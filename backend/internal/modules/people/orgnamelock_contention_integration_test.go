// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package people

// Which organization writers actually wait on the name lock, judged at the CALL
// SITE rather than on the helper that decides it.
//
// Which lock a writer holds, and in which order, is a property of the
// transaction rather than of any predicate, so it is measured here rather than
// asserted about a helper: the lock is held by hand in one transaction, the
// write runs in another, and Postgres is asked who waits on whom. No sleep and
// no clock — the same pg_stat_activity busy-read the phone-lane contention test
// uses.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// holdOrgNameLock opens a transaction holding the workspace's organization-name
// write identity and hands it back still open, so the test owns the moment a
// second writer can proceed. Its rollback is registered here: a transaction
// left open on a failure path holds the lock and a pooled connection with it,
// and the run that meant to fail loudly would hang instead.
func (e *dedupeEnv) holdOrgNameLock(ctx context.Context, t *testing.T) (pgx.Tx, int) {
	t.Helper()
	tx, err := e.store.db.Pool().Begin(ctx)
	if err != nil {
		t.Fatalf("opening the lock holder's transaction: %v", err)
	}
	t.Cleanup(func() {
		err := tx.Rollback(context.Background())
		if err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			t.Errorf("releasing the lock holder's transaction: %v", err)
		}
	})
	if _, err := tx.Exec(ctx, `SELECT set_config('app.workspace_id', $1, true)`, e.ws.String()); err != nil {
		t.Fatalf("binding the workspace GUC: %v", err)
	}
	var pid int
	if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatalf("reading the lock holder's backend pid: %v", err)
	}
	// Through the production helper, so a change to the key can never leave
	// this transaction holding something no writer waits on.
	if err := lockOrgNameWrites(ctx, tx); err != nil {
		t.Fatalf("taking the organization-name write identity: %v", err)
	}
	return tx, pid
}

// An apply that carries a legal name owes the name lock BEFORE it touches the
// organization row, so it cannot hold that row while wanting the lock — the
// cycle a concurrent human rename completes.
//
// The case is an apply CLEARING a name it is allowed to overwrite. That is the
// one an implementation is most likely to wave through as "no name here": it
// carries an empty value, yet it writes the row like any other write and lands
// in the duplicate re-check, which is what wants the lock. A non-empty value
// would prove the ordering too, but it would not tell a gate that reads the
// VALUE apart from one that reads the FIELD — and that is the distinction the
// ordering rests on.
func TestAnEvidenceApplyClearingANameWaitsOnTheNameLock(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	legal := "Kranz Logistik GmbH"
	org, err := e.store.CreateOrganization(ctx, CreateOrganizationInput{
		DisplayName: "Kranz Logistik", LegalName: &legal, Source: "manual",
		Domains: []OrgDomainInput{{Domain: "kranz.test", IsPrimary: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	orgID := ids.From[ids.OrganizationKind](ids.UUID(org.Id))

	holder, pid := e.holdOrgNameLock(ctx, t)

	done := make(chan error, 1)
	go func() {
		done <- e.store.tx(ctx, func(tx pgx.Tx) error {
			by, err := storekit.CapturedBy(ctx)
			if err != nil {
				return err
			}
			// Overwrite, so the clear actually reaches the column: the fill
			// arm has nothing to fill and would write no row at all.
			_, err = applyEvidenceFieldsWithOverwrite(ctx, tx, workspaceID(ctx), orgID, "site_read", by,
				[]ColdStartFieldInput{{
					Field: fieldLegalName, Value: "",
					EvidenceSnippet: "the imprint no longer states a registered name",
					SourceURL:       "https://kranz.test/impressum", Confidence: 1,
				}},
				map[string]bool{fieldLegalName: true})
			return err
		})
	}()

	if waited, finished := waitUntilBlockedBy(t, holder, pid, done); !waited {
		t.Fatalf("the apply completed without ever waiting on the name lock (err=%v) — "+
			"it can take this organization's row lock first and deadlock against a rename", finished)
	}

	// WHERE it is blocked is the whole question, and "it waited" cannot answer
	// it: an apply that takes the row lock first also ends up waiting, just one
	// statement too late.
	assertParkedBeforeTheOrganizationRow(ctx, t, holder, pid)

	// Releasing the holder must let it through, or the wait was on something
	// else entirely.
	if err := holder.Rollback(ctx); err != nil {
		t.Fatalf("releasing the lock holder: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the apply failed once the lock was free: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the apply never finished after the lock was released")
	}
}

// TestAnEvidenceApplyWithoutANameDoesNotWaitOnTheNameLock is the other half:
// the lock is workspace-wide, so an apply that cannot rename anything must not
// serialize behind every organization write in the installation. Enrichment and
// deep-read arrive in batches of exactly this shape.
func TestAnEvidenceApplyWithoutANameDoesNotWaitOnTheNameLock(t *testing.T) {
	e := setupDedupe(t)
	ctx := e.as()
	org, err := e.store.CreateOrganization(ctx, CreateOrganizationInput{
		DisplayName: "Vogel Werke", Source: "manual",
		Domains: []OrgDomainInput{{Domain: "vogel.test", IsPrimary: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	orgID := ids.From[ids.OrganizationKind](ids.UUID(org.Id))

	holder, pid := e.holdOrgNameLock(ctx, t)

	done := make(chan error, 1)
	go func() {
		done <- e.store.tx(ctx, func(tx pgx.Tx) error {
			by, err := storekit.CapturedBy(ctx)
			if err != nil {
				return err
			}
			_, err = applyEvidenceFields(ctx, tx, workspaceID(ctx), orgID, "site_read", by,
				[]ColdStartFieldInput{{
					Field: "industry", Value: "logistics",
					EvidenceSnippet: "Spedition und Logistik seit 1974",
					SourceURL:       "https://vogel.test/about", Confidence: 1,
				}})
			return err
		})
	}()

	waited, finished := waitUntilBlockedBy(t, holder, pid, done)
	if waited {
		t.Fatal("an apply carrying no name waited on the workspace-wide name lock — " +
			"every industry or address batch would serialize behind unrelated renames")
	}
	if finished != nil {
		t.Fatalf("the apply failed: %v", finished)
	}
}

// assertParkedBeforeTheOrganizationRow states the invariant DIRECTLY, by asking
// Postgres's lock manager where the waiter is — and it contains no clock.
//
// It replaces a `SET LOCAL lock_timeout = '5s'` and a `SELECT … FOR UPDATE` from
// the holder, which inferred the answer from whether that statement returned
// within five seconds. That inference had the timeout doing two jobs at once —
// bounding the test AND being the assertion — so a busy cluster produced a red
// run whose message said the lock ordering had regressed. The observed failure
// took 4.67s, right at the boundary, under a lane running 29 packages against
// one server. A flake that cries wolf about a deadlock ordering is worse than a
// plain flake: the next person either burns a day on a live bug that is not
// there, or learns to disbelieve the one message that must never be disbelieved.
//
// pg_locks is exempt from the frozen-snapshot trap that #970 fixed in this
// package's other probes: it reads the lock manager directly and is NOT the
// pg_stat_activity statistics snapshot, which is materialized once per
// transaction and cached until it ends. That is why this is the honest fix
// rather than merely a different poll, and the next author will otherwise
// assume the two views behave alike.
//
// Two facts are asserted, and both are needed. That the waiter is parked on the
// advisory lock says it reached the name lock at all; that it holds no write
// lock on `organization` says it had not touched the row first. Either alone
// passes over the defect: an apply parked on the row lock is also "waiting", and
// an apply that never reached the lock holds no organization lock either.
func assertParkedBeforeTheOrganizationRow(ctx context.Context, t *testing.T, holder pgx.Tx, holderPID int) {
	t.Helper()

	// The waiter, found through the lock manager rather than through
	// pg_blocking_pids: every backend queued on an advisory lock this holder
	// has been granted. The holder's own row is excluded by `granted`.
	var waiter int
	err := holder.QueryRow(ctx, `
		SELECT w.pid
		  FROM pg_locks w
		  JOIN pg_locks h
		    ON h.locktype = 'advisory' AND h.granted
		   AND h.classid = w.classid AND h.objid = w.objid AND h.objsubid = w.objsubid
		 WHERE w.locktype = 'advisory' AND NOT w.granted AND h.pid = $1
		 LIMIT 1`, holderPID).Scan(&waiter)
	if err != nil {
		t.Fatalf("reading the lock manager for a backend queued on the name lock: %v", err)
	}

	// Which locks that backend holds on the organization table. AccessShareLock
	// is a plain read and does not block a rename; RowShareLock and anything
	// above it means it has taken the row a rename would edit, which is the
	// cycle. Reported as the set, so a failure names what it found rather than
	// asserting a boolean.
	rows, err := holder.Query(ctx, `
		SELECT mode FROM pg_locks
		 WHERE pid = $1 AND locktype = 'relation' AND granted
		   AND relation = 'organization'::regclass
		   AND mode <> 'AccessShareLock'
		 ORDER BY mode`, waiter)
	if err != nil {
		t.Fatalf("reading backend %d's locks on the organization table: %v", waiter, err)
	}
	defer rows.Close()
	var held []string
	for rows.Next() {
		var mode string
		if err := rows.Scan(&mode); err != nil {
			t.Fatalf("scanning backend %d's lock modes: %v", waiter, err)
		}
		held = append(held, mode)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading backend %d's lock modes: %v", waiter, err)
	}
	if len(held) > 0 {
		t.Fatalf("the apply is parked on the name lock while already holding %v on the organization table — "+
			"it took the row a concurrent rename would edit BEFORE the lock that rename waits on, "+
			"which is the cycle that deadlocks", held)
	}
}
