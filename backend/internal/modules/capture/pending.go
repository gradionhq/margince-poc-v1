// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The capture disposition ledger (CAP-DDL-8, ADR-0072 §5). The tiered creation
// gate defers the ambiguous first-time sender instead of creating on sight: the
// Sink records what it decided about an address, IN the capture transaction, and
// the verdict engine resolves what it deferred. Suppressions record here too, so
// a wrong registry entry is queryable rather than only a log line.
//
// This file owns the ledger's SQL and nothing else — capture never touches
// person/organization tables, and the resolver seam stays the only way records
// come into being.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// Disposition statuses. `pending` and `unsure` are the LIVE states the unique
// index keys on — one open question per address at a time.
const (
	PendingStatusPending    = "pending"
	PendingStatusUnsure     = "unsure"
	PendingStatusReal       = "real"
	PendingStatusNoise      = "noise"
	PendingStatusSuppressed = "suppressed"
	// PendingStatusRejected records a human's decline. The approvals engine has
	// no reject hook — it runs only the approved branch — so the ledger learns
	// of a decline by reconciling against the approval row (ReconcileDeclined)
	// rather than by being told.
	PendingStatusRejected = "rejected"
)

// PendingMaxAttempts bounds the verdict retries (ADR-0072 §5: retries=2). A row
// that exhausts them is retired to `unsure` by RetireExhausted rather than
// retried forever — exhaustion is a terminal state, never a row nothing will
// ever pick up again.
// Exported so the verdict engine can retire a row deliberately — a terminal
// answer is reached by spending the attempts, not by a second spelling of
// "give up".
const PendingMaxAttempts = 2

// The bounds on what a row carries out of the ledger. Subject, body and display
// name are all straight from the message headers, which an outsider writes and
// no format limits: a folded Subject can be megabytes. Bounding them in SQL
// rather than at prompt-assembly time keeps the unbounded text out of the
// worker's memory as well as out of the model's context, and stops a mail run
// from turning into the workspace's whole model budget. The display name is
// bounded on the review path too, where it reaches a staged proposal and the
// SAR export rather than a prompt.
const (
	MaxCapturedSubjectChars = 300
	MaxCapturedBodyChars    = 1200
	// The display name gets its own bound rather than borrowing the subject's:
	// they are different fields on different paths, and a later tuning of one
	// must not silently move the other.
	MaxCapturedNameChars = 300
)

// pendingLease is how long a claimed row stays off other workers' scans. It has
// to cover the WHOLE claim: each row is judged on its own call and may be
// re-asked once, so a claim of verdictClaimSize rows can be a couple of dozen
// sequential model calls on a slow provider. A lease that expires mid-loop would
// let another replica re-claim a row this worker is still judging, and every
// claim spends an attempt — so the row would pay twice for one question.
const pendingLease = 45 * time.Minute

// NoiseUndoWindow is how long a noise-dispositioned message stays merely hidden
// before its content is redacted (ADR-0072 §4). The delay is the whole safety
// margin on the one verdict that destroys anything: a wrong `noise` is fully
// recoverable — un-archive and the mail is back — right up until the window
// closes, which is why hiding is allowed to be automatic at all.
const NoiseUndoWindow = 7 * 24 * time.Hour

// PendingCounterparty is one deferred disposition as the verdict engine reads
// it: the identity to judge and the message that raised the question.
type PendingCounterparty struct {
	ID          ids.UUID
	Email       string
	Domain      string
	DisplayName string // untrusted header text — for display, never matching
	ActivityID  ids.UUID
	OwnerID     ids.UUID
	Subject     string
	Body        string

	// Claim is this lease's token, minted by the ClaimDue that handed the row
	// out. Every write back to the ledger presents it, so a worker holding an
	// expired lease can no longer resolve a row that someone else has since
	// claimed. Carry it; never construct one.
	Claim ids.UUID
}

// recordDisposition writes one ledger row inside the caller's capture
// transaction. Idempotent on the live-row index: a second message from the same
// stranger joins the open question instead of queuing a second verdict for it.
//
// status decides whether anything is deferred — a T2 suppression records its
// reason and retires immediately (no next_attempt_at), while a T4 ambiguous
// sender stays due for the verdict engine.
//
// It reports which deferral ceiling refused the row (empty for none), which the
// caller records as its own breadcrumb: a capture that asks no question is a
// different event from one that joins an existing one, and only the first means
// the workspace is being flooded.
//
// The erasure-suppression probe below has no channel twin, and that is
// structural rather than an omission: this ledger is keyed on an address, so a
// record identified by a channel identity can never reach it. The channel key's
// own refusal lives where such a record does go — people's
// EnsureChannelCounterparty, through storekit.ChannelIdentitySuppressed.
func recordDisposition(ctx context.Context, tx pgx.Tx, in dispositionRow) (string, error) {
	email := normalizeEmail(in.Email)
	if email == "" {
		return "", errors.New("capture: a disposition needs a normalized counterparty address")
	}
	// Deletion sticks, at the WRITE and not only in the erasure sweep. An erased
	// subject's address must not re-materialize here — a fresh ledger row would
	// restore their address and header display name in a new table, and a
	// deferred row additionally hands their subject and body to the routed model
	// provider on the verdict call. The two sibling paths (captureLead,
	// EnsureCounterpartyTx) already refuse a suppressed address; this is the
	// same invariant, not a new rule.
	suppressed, err := storekit.EmailSuppressed(ctx, tx, email)
	if err != nil {
		return "", fmt.Errorf("capture: checking the suppression list: %w", err)
	}
	if suppressed {
		return "", nil
	}

	due := in.Status == PendingStatusPending
	if due {
		// Asked before the insert rather than folded into it as a WHERE, because
		// the two zero-row outcomes must stay distinguishable: at the cap nothing
		// is asked, whereas ON CONFLICT DO NOTHING means the question is already
		// open. The count is exact under RLS (the policy scopes it to this
		// workspace) and only the ambiguous tier pays for it.
		//
		// The ceiling applies to NEW questions only. Once it is reached, every
		// further message from any of the already-deferred senders would
		// otherwise be reported as capped-and-unjudged, when its question is in
		// fact open and will be answered — a breadcrumb that misdescribes the
		// system is worse than none.
		//
		// Serialized per workspace for the rest of this transaction, because
		// counting and inserting are two statements: without the lock a burst of
		// first-time senders all read a count below the ceiling and all insert,
		// so the bound is exceeded by however many captures were in flight —
		// which is precisely the flood the ceiling exists to stop. Taken only on
		// the ambiguous tier (the rare one) and only after the suppression
		// check, so ordinary capture never queues behind it.
		if err := lockWorkspaceDeferrals(ctx, tx); err != nil {
			return "", err
		}
		capped, err := capRefusesNewQuestion(ctx, tx, email, in.Domain)
		if err != nil {
			return "", err
		}
		if capped != "" {
			return capped, nil
		}
	}
	// One disposition per address per state, whichever index arbitrates it: a
	// second message from the same stranger joins the open question, and a
	// second newsletter does not append another copy of the same answer.
	conflict := "(workspace_id, email) WHERE status IN ('pending', 'unsure')"
	if in.Status == PendingStatusSuppressed {
		conflict = "(workspace_id, email) WHERE status = 'suppressed'"
	}
	// Due-ness is stamped with the DATABASE's clock, never the caller's. ClaimDue
	// compares next_attempt_at against Postgres now(), so a next_attempt_at taken
	// from the app process makes the comparison a cross-clock one: an app running
	// even milliseconds ahead of the database writes a row that is not yet due and
	// silently waits out the skew before anything claims it.
	_, err = tx.Exec(ctx, `
		INSERT INTO capture_pending_counterparty
		  (workspace_id, email, domain, display_name, activity_id, owner_id, status,
		   disposition_reason, next_attempt_at)
		VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid,
		        $1, NULLIF($2, ''), NULLIF($3, ''), $4, $5, $6, NULLIF($7, ''),
		        CASE WHEN $8::boolean THEN now() END)
		ON CONFLICT `+conflict+`
		DO NOTHING`,
		email, strings.ToLower(strings.TrimSpace(in.Domain)), in.DisplayName,
		in.ActivityID, in.OwnerID, in.Status, in.Reason, due)
	if err != nil {
		return "", fmt.Errorf("capture: recording the counterparty disposition: %w", err)
	}
	return "", nil
}

// dispositionRow names one ledger write.
type dispositionRow struct {
	Email       string
	Domain      string
	DisplayName string
	ActivityID  ids.UUID
	OwnerID     ids.UUID
	Status      string
	Reason      string
}

// PendingStore reads and resolves the ledger. It is the verdict engine's seam
// into capture's own table; compose injects it.
type PendingStore struct{ pool *pgxpool.Pool }

// NewPendingStore builds the ledger store over the capture pool.
func NewPendingStore(pool *pgxpool.Pool) *PendingStore { return &PendingStore{pool: pool} }

// ClaimDue atomically leases up to limit due rows for this workspace. FOR UPDATE
// SKIP LOCKED lets several replicas drain the ledger without double-judging a
// row or serializing on each other; the lease is what a crashed worker releases
// by expiry.
//
// Claiming bumps attempts, so a row that keeps failing walks toward its bound
// rather than being retried forever, and stamps a fresh claim token every
// batch shares — the key Resolve and Defer demand back.
func (s *PendingStore) ClaimDue(ctx context.Context, limit int) ([]PendingCounterparty, error) {
	claim := ids.NewV7()
	var out []PendingCounterparty
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			UPDATE capture_pending_counterparty p
			   SET attempts = p.attempts + 1,
			       claimed_until = now() + make_interval(secs => $2),
			       claimed_by = $4,
			       updated_at = now()
			 WHERE p.id IN (
			   SELECT id FROM capture_pending_counterparty
			    WHERE status = 'pending'
			      AND next_attempt_at IS NOT NULL AND next_attempt_at <= now()
			      AND (claimed_until IS NULL OR claimed_until <= now())
			      -- The bound is a property of the ROW, not of a live worker.
			      -- A worker that crashes, is killed, or outruns its lease never
			      -- reaches Defer, so a row whose content reliably kills the
			      -- verdict step would otherwise be re-claimed every lease
			      -- expiry forever, at one model call a time.
			      AND attempts < $3
			    ORDER BY next_attempt_at
			    LIMIT $1
			    FOR UPDATE SKIP LOCKED)
			RETURNING p.id, p.email, coalesce(p.domain, ''), coalesce(left(p.display_name, $5), ''),
			          p.activity_id, p.owner_id,
			          coalesce(left((SELECT a.subject FROM activity a WHERE a.id = p.activity_id), $6), ''),
			          coalesce(left((SELECT a.body FROM activity a WHERE a.id = p.activity_id), $7), '')`,
			limit, pendingLease.Seconds(), PendingMaxAttempts, claim,
			MaxCapturedNameChars, MaxCapturedSubjectChars, MaxCapturedBodyChars)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			p := PendingCounterparty{Claim: claim}
			if err := rows.Scan(&p.ID, &p.Email, &p.Domain, &p.DisplayName,
				&p.ActivityID, &p.OwnerID, &p.Subject, &p.Body); err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("capture: claiming due dispositions: %w", err)
	}
	return out, nil
}

// Resolve closes a claimed row with its verdict. The CAS on `pending` AND on the
// caller's own claim is what makes a racing second worker — or a replayed job,
// or one whose lease expired while it was still running — a no-op rather than a
// second creation: it reports whether THIS call was the one that resolved the
// row, and only that caller may act on the verdict.
//
// It takes the claimed row rather than an id so the token cannot be lost or
// mismatched on the way here.
func (s *PendingStore) Resolve(ctx context.Context, tx pgx.Tx, p PendingCounterparty, status, reason string) (bool, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE capture_pending_counterparty
		   SET status = $2, disposition_reason = NULLIF($3, ''),
		       resolved_at = now(), next_attempt_at = NULL,
		       claimed_until = NULL, claimed_by = NULL, updated_at = now()
		 WHERE id = $1 AND status = 'pending' AND claimed_by = $4`, p.ID, status, reason, p.Claim)
	if err != nil {
		return false, fmt.Errorf("capture: resolving disposition %s: %w", p.ID, err)
	}
	return tag.RowsAffected() == 1, nil
}

// CorrectResolution moves a row from the status this caller just wrote to a
// corrected one, on the same transaction that wrote it.
//
// It exists because the claim CAS is spent by then: Resolve clears claimed_by,
// so a second Resolve for the corrected status would match nothing and report
// success — leaving the ledger asserting a disposition the caller had already
// discovered was wrong. This CASes on the status instead, which is the thing
// that is still true inside this transaction.
func (s *PendingStore) CorrectResolution(ctx context.Context, tx pgx.Tx, id ids.UUID, from, to, reason string) error {
	tag, err := tx.Exec(ctx, `
		UPDATE capture_pending_counterparty
		   SET status = $3, disposition_reason = NULLIF($4, ''), updated_at = now()
		 WHERE id = $1 AND status = $2`, id, from, to, reason)
	if err != nil {
		return fmt.Errorf("capture: correcting disposition %s: %w", id, err)
	}
	if tag.RowsAffected() != 1 {
		// The caller wrote `from` moments ago on this very transaction, so a
		// miss means the two have drifted apart — worth failing the write
		// rather than committing a status nobody intended.
		return fmt.Errorf("capture: correcting disposition %s: expected status %q", id, from)
	}
	return nil
}

// Defer returns a claimed row to the queue for a later pass. Ending a row is
// Retire's job, not this one: a deferral says "ask again later", and conflating
// the two is how a row ends up retired for reasons that had nothing to do with
// the question (a provider outage, a budget stop). refundAttempt says whether
// this cause reached a model — see the note on the UPDATE.
//
// Guarded by the same claim as Resolve, for the same reason: a stalled worker
// releasing "its" row would otherwise cut short a lease someone else now holds.
func (s *PendingStore) Defer(ctx context.Context, p PendingCounterparty, backoff time.Duration, reason string, refundAttempt bool) error {
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Whether the attempt is given back is the difference between a row the
		// SYSTEM failed and a row that fails on its own content.
		//
		// A budget stop never reached a model, so charging for it would let two
		// quiet cycles exhaust an address's allowance with no verdict ever
		// attempted on its merits — that one is refunded. A reply the validator
		// rejected DID reach a model, and its cause is the message itself, which
		// an outsider writes: refunding those would make the attempt bound
		// unreachable on exactly the path it exists for, and a sender whose text
		// reliably breaks the answer would be re-judged forever at one paid call
		// a time.
		_, err := tx.Exec(ctx, `
			UPDATE capture_pending_counterparty
			   SET next_attempt_at = now() + make_interval(secs => $2),
			       attempts = CASE WHEN $5::boolean THEN greatest(attempts - 1, 0) ELSE attempts END,
			       disposition_reason = NULLIF($4, ''),
			       claimed_until = NULL, claimed_by = NULL, updated_at = now()
			 WHERE id = $1 AND status = 'pending' AND claimed_by = $3`,
			p.ID, backoff.Seconds(), p.Claim, reason, refundAttempt)
		return err
	})
	if err != nil {
		return fmt.Errorf("capture: deferring disposition %s: %w", p.ID, err)
	}
	return nil
}

// Retire ends a claimed row at `unsure`: the model was asked its allowance of
// times and never cleared the floor, so the question passes to a human.
//
// Terminal by construction: it stamps the attempt count it asserts and the time
// it stopped, so an operator reading the row sees why it ended rather than
// having to infer it from a counter that says something else.
func (s *PendingStore) Retire(ctx context.Context, p PendingCounterparty, reason string) error {
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE capture_pending_counterparty
			   SET status = 'unsure', disposition_reason = NULLIF($2, ''),
			       attempts = $4, resolved_at = now(),
			       next_attempt_at = NULL, claimed_until = NULL, claimed_by = NULL,
			       updated_at = now()
			 WHERE id = $1 AND status = 'pending' AND claimed_by = $3`,
			p.ID, reason, p.Claim, PendingMaxAttempts)
		return err
	})
	if err != nil {
		return fmt.Errorf("capture: retiring disposition %s: %w", p.ID, err)
	}
	return nil
}

// normalizeEmail is the ONE spelling of the ledger's identity: lowercased and
// trimmed, matching activity.counterparty_email and person_email so the verdict,
// the correspondence gate, and the dedupe chokepoint agree on what the same
// address is.
func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}
