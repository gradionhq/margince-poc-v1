// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integrations

// Reconciliation settles a hold against what the provider actually charged,
// and the billing basis decides what a no-match does to it. Both arms are
// money: releasing a hold the provider kept understates the bill, and holding
// one it refunded silently shrinks the customer's ceiling for the month.

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/ports/provider"
)

// reserveOneRun queues a run through the real pipeline and returns its id, so
// the reservations under test are the ones production writes.
func reserveOneRun(t *testing.T, e *runsEnv) string {
	t.Helper()
	run, err := e.store.QueueRun(e.ctx, provider.QueueInput{
		PersonID: e.mine.String(), Provider: "surfe", Trigger: provider.TriggerManual,
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.State != provider.RunQueued {
		t.Fatalf("run is %s, want queued", run.State)
	}
	return run.ID
}

func creditsFor(t *testing.T, e *runsEnv, runID, pool string) (reserved int, actual *int) {
	t.Helper()
	if err := e.owner.QueryRow(context.Background(),
		`SELECT reserved_credits, actual_credits FROM provider_run_reservation
		  WHERE run_id = $1 AND pool = $2`, runID, pool).Scan(&reserved, &actual); err != nil {
		t.Fatal(err)
	}
	return reserved, actual
}

// A provider that charges only for a successful result owes nothing on a
// no-match, so the whole hold is released.
func TestReconcileReleasesTheHoldWhenNothingWasBought(t *testing.T) {
	e := setupRuns(t, runsConfig{})
	runID := reserveOneRun(t, e)
	desc := NewOfflineProvider(0, e.store.now).Descriptor()

	if err := e.store.db.Tx(e.ctx, func(tx pgx.Tx) error {
		return e.store.reconcile(e.ctx, tx, desc, runID, nil, false)
	}); err != nil {
		t.Fatal(err)
	}

	_, actual := creditsFor(t, e, runID, "email")
	if actual == nil || *actual != 0 {
		t.Errorf("actual_credits = %v, want 0 — a per-successful-result provider refunds a no-match, and holding those credits would shrink the customer's ceiling for nothing", actual)
	}
}

// On a match, the provider's own number wins where it gave one.
func TestReconcileRecordsWhatTheProviderActuallyCharged(t *testing.T) {
	e := setupRuns(t, runsConfig{})
	runID := reserveOneRun(t, e)
	desc := NewOfflineProvider(0, e.store.now).Descriptor()

	if err := e.store.db.Tx(e.ctx, func(tx pgx.Tx) error {
		return e.store.reconcile(e.ctx, tx, desc, runID,
			map[provider.Pool]int{"email": 1, "mobile": 1}, true)
	}); err != nil {
		t.Fatal(err)
	}

	for _, pool := range []string{"email", "mobile"} {
		_, actual := creditsFor(t, e, runID, pool)
		if actual == nil || *actual != 1 {
			t.Errorf("%s actual_credits = %v, want 1", pool, actual)
		}
	}
}

// Where the provider said nothing about a pool, the hold stands as spent:
// assuming a refund nobody promised understates what the customer was charged.
func TestReconcileTreatsSilenceAsSpent(t *testing.T) {
	e := setupRuns(t, runsConfig{})
	runID := reserveOneRun(t, e)
	desc := NewOfflineProvider(0, e.store.now).Descriptor()

	if err := e.store.db.Tx(e.ctx, func(tx pgx.Tx) error {
		// A match, but the provider reported no per-pool cost at all.
		return e.store.reconcile(e.ctx, tx, desc, runID, nil, true)
	}); err != nil {
		t.Fatal(err)
	}

	reserved, actual := creditsFor(t, e, runID, "email")
	if actual == nil || *actual != reserved {
		t.Errorf("actual_credits = %v, want the reserved %d — silence is not a refund", actual, reserved)
	}
}
