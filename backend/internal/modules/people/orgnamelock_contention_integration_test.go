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
	tx, err := e.store.pool.Begin(ctx)
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
	// statement too late. So the holder — which owns the name lock a human
	// rename would hold — reaches for the row that rename would edit. If the
	// apply is parked before touching the row, this succeeds at once. If it is
	// parked while holding the row, the two are in the cycle that deadlocks and
	// this blocks until the timeout.
	if _, err := holder.Exec(ctx, `SET LOCAL lock_timeout = '5s'`); err != nil {
		t.Fatalf("arming the lock timeout: %v", err)
	}
	if _, err := holder.Exec(ctx,
		`SELECT id FROM organization WHERE id = $1 FOR UPDATE`, orgID); err != nil {
		t.Fatalf("the rename could not take the organization row while the apply waited (%v) — "+
			"the apply is holding that row and waiting for the name lock, the ordering that deadlocks", err)
	}

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
