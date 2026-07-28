// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The disposition ledger's two safety properties (ADR-0072 §5), both of which
// are about an adversary rather than a bug: an OUTSIDER creates ledger rows by
// mailing the connected mailbox, so the queue needs a ceiling; and a claimed row
// is worked by a fallible worker, so a lease that only expires is not a lease at
// all. Neither can be proven without a real Postgres — the first is a count
// under RLS, the second a compare-and-set race.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// A workspace at its open-question ceiling stops queueing verdicts, and says so.
// The ceiling exists because the party filling the queue is whoever can mail the
// mailbox: without it, a stranger sending from fresh addresses decides how much
// the workspace spends on model calls.
func TestCaptureLedgerStopsDeferringAtTheWorkspaceCeiling(t *testing.T) {
	env := newCaptureEnv(t)
	e, sync := env.e, env.sync

	// One real capture first — it defers, and gives us the activity and owner a
	// synthetic backlog can hang off.
	sync(t, email("first@stranger.example", "First Stranger", captureOwner, "led1@stranger.example", ""))
	var activityID, ownerID ids.UUID
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `
			SELECT activity_id, owner_id FROM capture_pending_counterparty
			 WHERE email = 'first@stranger.example'`).Scan(&activityID, &ownerID)
	}); err != nil {
		t.Fatalf("reading the seeded disposition: %v", err)
	}

	// Fill the queue to exactly the ceiling. The rows are synthetic on purpose:
	// what matters is the count the gate reads, not how each one got there.
	fillLedgerToCeiling(t, e, activityID, ownerID)

	sync(t, email("late@stranger.example", "Late Stranger", captureOwner, "led2@stranger.example", ""))

	if n := countRows(t, e, `
		SELECT count(*) FROM capture_pending_counterparty
		WHERE email = 'late@stranger.example'`); n != 0 {
		t.Fatalf("%d ledger rows past the ceiling, want 0 — the cap must refuse the deferral", n)
	}
	// The message is not the thing being refused. Dropping it would lose mail to
	// protect a budget; only the QUESTION is declined.
	if n := countRows(t, e, `SELECT count(*) FROM activity WHERE source_id = 'led2@stranger.example'`); n != 1 {
		t.Fatalf("%d activities for the capped message, want 1 — the timeline row must stand", n)
	}
	if n := countRows(t, e, `
		SELECT count(*) FROM system_log
		WHERE action = 'capture_deferral_capped' AND detail->>'source_id' = 'led2@stranger.example'`); n != 1 {
		t.Fatalf("%d cap breadcrumbs, want 1 — an unasked question must not look like a dismissed one", n)
	}
	// And no record was minted as a consolation: at the cap the sender stays
	// exactly as unknown as it was.
	if n := countRows(t, e, `
		SELECT count(*) FROM person p JOIN person_email pe ON pe.person_id = p.id
		WHERE pe.email = 'late@stranger.example'`); n != 0 {
		t.Fatal("a capped deferral created the person it declined to ask about")
	}
}

// A worker whose lease expired must not be able to write its verdict, because
// by then the row may have been re-claimed and re-judged by someone else. Expiry
// alone cannot express that: the row is 'pending' again, so a status-only
// compare-and-set accepts the zombie's answer and silently overwrites the live
// one. The claim token is what makes the second write lose.
func TestCaptureLedgerRefusesAVerdictFromAnExpiredClaim(t *testing.T) {
	env := newCaptureEnv(t)
	e, sync := env.e, env.sync
	store := capture.NewPendingStore(e.Pool)
	wsCtx := principal.WithWorkspaceID(context.Background(), e.WS)

	sync(t, email("contested@stranger.example", "Contested", captureOwner, "led3@stranger.example", ""))

	stale, err := store.ClaimDue(wsCtx, 10)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if len(stale) != 1 {
		t.Fatalf("claimed %d rows, want 1", len(stale))
	}

	// The first worker stalls past its lease — expressed as the row becoming
	// claimable again, which is what a wall-clock expiry means to every other
	// worker. Forced rather than waited for: a test that sleeps out a 45-minute
	// lease proves the same thing 15 minutes later.
	expireClaimLease(t, e, stale[0].ID)

	fresh, err := store.ClaimDue(wsCtx, 10)
	if err != nil {
		t.Fatalf("re-claim after expiry: %v", err)
	}
	if len(fresh) != 1 {
		t.Fatalf("re-claimed %d rows, want 1 — an expired lease must return the row to the queue", len(fresh))
	}
	if fresh[0].Claim == stale[0].Claim {
		t.Fatal("the re-claim reused the expired claim token — every lease must be its own key")
	}

	// The zombie finishes and reports. It must lose.
	resolved := resolveWith(t, e, store, stale[0], capture.PendingStatusReal)
	if resolved {
		t.Fatal("a worker with an expired claim resolved a row another worker now holds")
	}
	// And the live holder must still be able to resolve it — a token that
	// rejected everyone would pass the test above and deadlock the queue.
	if !resolveWith(t, e, store, fresh[0], capture.PendingStatusNoise) {
		t.Fatal("the live claim could not resolve its own row")
	}
	if n := countRows(t, e, `
		SELECT count(*) FROM capture_pending_counterparty
		WHERE email = 'contested@stranger.example' AND status = 'noise'`); n != 1 {
		t.Fatal("the surviving verdict is not the one the live claim wrote")
	}
}

// resolveWith runs one Resolve on its own transaction, as the verdict engine
// does, and reports whether this caller was the one that closed the row.
func resolveWith(t *testing.T, e *searchEnv, store *capture.PendingStore, p capture.PendingCounterparty, status string) bool {
	t.Helper()
	var resolved bool
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		var err error
		resolved, err = store.Resolve(context.Background(), tx, p, status, "test verdict")
		return err
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return resolved
}

// expireClaimLease backdates a row's lease so it is claimable again — the state
// a crashed or stalled worker leaves behind.
func expireClaimLease(t *testing.T, e *searchEnv, id ids.UUID) {
	t.Helper()
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			UPDATE capture_pending_counterparty
			   SET claimed_until = now() - interval '1 minute'
			 WHERE id = $1`, id)
		return err
	})
	if err != nil {
		t.Fatalf("expiring the claim lease: %v", err)
	}
}

// fillLedgerToCeiling tops the workspace up to exactly its open-disposition
// ceiling, leaving the queue full but not over — so the next capture is the
// first one the cap refuses.
func fillLedgerToCeiling(t *testing.T, e *searchEnv, activityID, ownerID ids.UUID) {
	t.Helper()
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		var live int
		if err := tx.QueryRow(context.Background(), `
			SELECT count(*) FROM capture_pending_counterparty
			 WHERE status IN ('pending', 'unsure')`).Scan(&live); err != nil {
			return err
		}
		_, err := tx.Exec(context.Background(), `
			INSERT INTO capture_pending_counterparty
			  (workspace_id, email, domain, activity_id, owner_id, status, next_attempt_at)
			SELECT $1, 'filler' || g || '@flood.example', 'flood.example', $2, $3, 'pending', now()
			  FROM generate_series(1, $4) g`,
			e.WS, activityID, ownerID, capture.PendingDeferralCap-live)
		return err
	})
	if err != nil {
		t.Fatalf("filling the ledger to the ceiling: %v", err)
	}
}

// One domain flooding the queue must not cost every other sender their
// deferral. The workspace ceiling alone fails that way and an outsider can steer
// it: mail from enough fresh addresses at one throwaway domain and no NEW
// corporate sender is ever deferred again. The per-domain share is what keeps a
// flood inside its own lane.
func TestCaptureLedgerStopsDeferringOneFloodingDomain(t *testing.T) {
	env := newCaptureEnv(t)
	e, sync := env.e, env.sync

	sync(t, email("first@flood.example", "First Flooder", captureOwner, "dom1@flood.example", ""))
	var activityID, ownerID ids.UUID
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `
			SELECT activity_id, owner_id FROM capture_pending_counterparty
			 WHERE email = 'first@flood.example'`).Scan(&activityID, &ownerID)
	}); err != nil {
		t.Fatalf("reading the seeded disposition: %v", err)
	}
	fillDomainToShare(t, e, "flood.example", activityID, ownerID)

	sync(t, email("late@flood.example", "Late Flooder", captureOwner, "dom2@flood.example", ""))
	if n := countRows(t, e, `
		SELECT count(*) FROM capture_pending_counterparty
		WHERE email = 'late@flood.example'`); n != 0 {
		t.Fatalf("%d ledger rows past the domain share, want 0", n)
	}
	// The breadcrumb names the domain ceiling, not the workspace one: an
	// operator reading "the queue is full" would go looking for the wrong thing.
	if n := countRows(t, e, `
		SELECT count(*) FROM system_log
		WHERE action = 'capture_deferral_capped'
		  AND detail->>'source_id' = 'dom2@flood.example'
		  AND detail->>'ceiling' = 'domain_ceiling'`); n != 1 {
		t.Fatalf("%d domain-ceiling breadcrumbs, want 1", n)
	}

	// And the point of the whole exercise: everyone else still gets through.
	sync(t, email("real@customer.example", "Real Prospect", captureOwner, "dom3@customer.example", ""))
	if n := countRows(t, e, `
		SELECT count(*) FROM capture_pending_counterparty
		WHERE email = 'real@customer.example'`); n != 1 {
		t.Fatalf("%d ledger rows for an unrelated sender, want 1 — one domain's flood must not cost everyone their deferral", n)
	}
}

// fillDomainToShare tops one domain up to exactly its share of the ceiling,
// leaving it full but not over — so the next message from it is the first the
// per-domain cap refuses.
func fillDomainToShare(t *testing.T, e *searchEnv, domain string, activityID, ownerID ids.UUID) {
	t.Helper()
	err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		var live int
		if err := tx.QueryRow(context.Background(), `
			SELECT count(*) FROM capture_pending_counterparty
			 WHERE domain = $1 AND status IN ('pending', 'unsure')`, domain).Scan(&live); err != nil {
			return err
		}
		_, err := tx.Exec(context.Background(), `
			INSERT INTO capture_pending_counterparty
			  (workspace_id, email, domain, activity_id, owner_id, status, next_attempt_at)
			SELECT $1, 'domfill' || g || '@' || $5, $5, $2, $3, 'pending', now()
			  FROM generate_series(1, $4) g`,
			e.WS, activityID, ownerID, capture.PendingDeferralDomainCap-live, domain)
		return err
	})
	if err != nil {
		t.Fatalf("filling the domain to its share: %v", err)
	}
}
