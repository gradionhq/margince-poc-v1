// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The enrich-on-capture trigger's decisions (ADR-0072/A118 §9).
//
// What these cover is the part that can go wrong quietly: which gates run, in
// which order, and — for every way the trigger gives up — whether the
// organization is left in a state the daily sweep still finds. That contract is
// the entire reason the trigger is allowed to be best-effort, so it is the thing
// worth holding.
//
// The budget counter these gates spend from is tested here too, next to the
// trigger that shares it with the sweep.
//
// What they do NOT cover is the queued read itself: starting one needs an
// ambient River client, and this repo has no harness that stands one up in a
// test. The same gap already applies to the sweep's own trigger path; the
// read-and-apply half is covered by TestAutoEnrichLaneAppliesDirectlyInsteadOfStaging.

import (
	"context"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/capture"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// stillDue reports whether the sweep would pick this organization up.
func stillDue(t *testing.T, e *integration.Env, org ids.OrganizationID) bool {
	t.Helper()
	due, err := capture.NewAutoEnrichStore(e.Pool).ListDueOrgs(e.Admin(), 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range due {
		if d.OrganizationID == org {
			return true
		}
	}
	return false
}

func setAutoEnrich(t *testing.T, e *integration.Env, on bool) {
	t.Helper()
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(),
			`UPDATE workspace SET capture_auto_enrich = $2 WHERE id = $1`, e.WS, on)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func budgetSpent(t *testing.T, e *integration.Env) int {
	t.Helper()
	return e.WsCount(t, `
		SELECT coalesce(sum(enqueued), 0) FROM capture_auto_enrich_budget`)
}

func TestEnrichOnCaptureRespectsTheSettingBeforeSpendingAnything(t *testing.T) {
	e := integration.Setup(t)
	setAutoEnrich(t, e, false)
	org := insertDomainOrg(t, e, "switched-off.example")

	trigger := newAutoEnrichTrigger(e.Pool, slog.New(slog.DiscardHandler))
	trigger.organizationCaptured(e.Admin(), org, "switched-off.example")

	if n := budgetSpent(t, e); n != 0 {
		t.Fatalf("%d budget slots spent with the setting off — the flag must be read before the budget", n)
	}
	if !stillDue(t, e, org) {
		t.Fatal("the organization was retired from the sweep by a trigger that did nothing")
	}
}

func TestEnrichOnCaptureLeavesTheOrgToTheSweepAtTheDailyCap(t *testing.T) {
	e := integration.Setup(t)
	setAutoEnrich(t, e, true)
	org := insertDomainOrg(t, e, "capped.example")

	// A cap of its own, not the shipped 500: filling the real one costs 500
	// round trips to demonstrate a bound that behaves identically at three, and
	// the number under test is "the cap", not its value.
	const testCap = 3
	store := capture.NewAutoEnrichStore(e.Pool)
	for i := 0; i < testCap; i++ {
		slot, err := store.ReserveBudget(e.Admin(), testCap)
		if err != nil {
			t.Fatal(err)
		}
		if !slot.Reserved {
			t.Fatalf("reservation %d refused before the cap", i)
		}
	}

	trigger := newAutoEnrichTrigger(e.Pool, slog.New(slog.DiscardHandler))
	trigger.dailyCap = testCap
	trigger.organizationCaptured(e.Admin(), org, "capped.example")

	if n := budgetSpent(t, e); n != testCap {
		t.Fatalf("budget spent = %d, want the cap %d — the trigger must not spend past it", n, testCap)
	}
	if !stillDue(t, e, org) {
		t.Fatal("a capped trigger retired the organization — it must stay due for a later sweep")
	}
}

// Every way the trigger can give up has to leave the organization findable by
// the sweep. This drives the give-up that is hardest to reason about — the read
// itself failing to start — by running with no ambient River client, which is
// exactly what a missing queue looks like from here.
func TestEnrichOnCaptureLeavesTheOrgToTheSweepWhenTheReadCannotStart(t *testing.T) {
	e := integration.Setup(t)
	setAutoEnrich(t, e, true)
	org := insertDomainOrg(t, e, "no-queue.example")

	trigger := newAutoEnrichTrigger(e.Pool, slog.New(slog.DiscardHandler))
	trigger.organizationCaptured(e.Admin(), org, "no-queue.example")

	if !stillDue(t, e, org) {
		t.Fatal("a trigger that could not start the read retired the organization anyway")
	}
	// And the slot it reserved goes back. Reserving before starting is what makes
	// the cap a cap; refunding what did not start is what stops the day's
	// allowance eroding a slot at a time on a path that never reads anything.
	if n := budgetSpent(t, e); n != 0 {
		t.Fatalf("budget spent = %d, want 0 — a slot that bought no read must be returned", n)
	}
}

// There is nothing to read without a domain, so nothing is reserved. The gate is
// first because it is the only one that needs no query; this asserts the spend,
// which is the part that matters, not the query count.
func TestEnrichOnCaptureIgnoresAnEmptyDomain(t *testing.T) {
	e := integration.Setup(t)
	setAutoEnrich(t, e, true)
	org := insertDomainOrg(t, e, "nodomain.example")

	trigger := newAutoEnrichTrigger(e.Pool, slog.New(slog.DiscardHandler))
	trigger.organizationCaptured(e.Admin(), org, "")

	if n := budgetSpent(t, e); n != 0 {
		t.Fatalf("%d budget slots spent for an organization with no domain to read", n)
	}
}

// Reserve-before-spend means a caller sometimes holds a slot it turns out not to
// need — two paths racing on one organization both reserve, and the in-flight
// uniqueness index lets only one of them start a read. The refund is what keeps
// the day's ten reads from delivering nine, with the shortfall growing with
// exactly the concurrency the cap is meant to be indifferent to.
func TestAutoEnrichBudgetSlotIsReturnedWhenItBoughtNothing(t *testing.T) {
	e := integration.Setup(t)
	store := capture.NewAutoEnrichStore(e.Pool)

	const testCap = 3
	var last capture.BudgetSlot
	for i := 0; i < testCap; i++ {
		slot, err := store.ReserveBudget(e.Admin(), testCap)
		if err != nil {
			t.Fatal(err)
		}
		if !slot.Reserved {
			t.Fatalf("setup reservation %d refused before the cap", i)
		}
		last = slot
	}
	if slot, err := store.ReserveBudget(e.Admin(), testCap); err != nil || slot.Reserved {
		t.Fatalf("reservation past the cap: reserved=%v err=%v", slot.Reserved, err)
	}

	if err := store.ReleaseBudget(e.Admin(), last); err != nil {
		t.Fatalf("ReleaseBudget: %v", err)
	}
	if n := budgetSpent(t, e); n != testCap-1 {
		t.Fatalf("budget spent = %d after a refund, want %d", n, testCap-1)
	}
	slot, err := store.ReserveBudget(e.Admin(), testCap)
	if err != nil {
		t.Fatal(err)
	}
	if !slot.Reserved {
		t.Fatal("the returned slot was not reusable — a refund that frees nothing is not a refund")
	}
}

// A refund can only ever return a slot that was taken. Letting the counter run
// below zero would hand out free reads on the next reservation, which is the
// failure the cap exists to prevent.
func TestAutoEnrichBudgetReleaseNeverGoesBelowZero(t *testing.T) {
	e := integration.Setup(t)
	store := capture.NewAutoEnrichStore(e.Pool)

	const testCap = 3
	// The day comes from a real reservation rather than Go's clock: the counter
	// is keyed on the DATABASE's UTC day, and a test that builds its own would
	// disagree with it across a midnight or any clock offset.
	today, err := store.ReserveBudget(e.Admin(), testCap)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < testCap; i++ {
		if err := store.ReleaseBudget(e.Admin(), today); err != nil {
			t.Fatalf("ReleaseBudget on an unspent day: %v", err)
		}
	}
	if n := budgetSpent(t, e); n != 0 {
		t.Fatalf("budget spent = %d after more refunds than reservations, want 0", n)
	}
	// And the day still gives its full allowance.
	for i := 0; i < testCap; i++ {
		slot, err := store.ReserveBudget(e.Admin(), testCap)
		if err != nil {
			t.Fatal(err)
		}
		if !slot.Reserved {
			t.Fatalf("reservation %d refused — a refund below zero stole from the day", i)
		}
	}
}

// The refund names the day the slot was taken on, not today. A read that starts
// at 23:59:59 and joins after midnight would otherwise decrement the NEW day's
// row — freeing a slot nobody took, and letting that day start one read past its
// cap.
func TestAutoEnrichBudgetRefundNamesTheDayItWasReservedOn(t *testing.T) {
	e := integration.Setup(t)
	store := capture.NewAutoEnrichStore(e.Pool)

	// Yesterday relative to the DATABASE's day, taken from a real reservation —
	// the counter is keyed on that day, not on the test process's clock.
	todaySlot, err := store.ReserveBudget(e.Admin(), 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReleaseBudget(e.Admin(), todaySlot); err != nil {
		t.Fatal(err)
	}
	yesterday := capture.BudgetSlot{Day: todaySlot.Day.AddDate(0, 0, -1), Reserved: true}
	e.WsExec(t, `
		INSERT INTO capture_auto_enrich_budget (workspace_id, budget_date, enqueued)
		VALUES ($1, $2, 1)`, e.WS, yesterday.Day)

	// Today has a reservation of its own — the slot that must survive.
	if _, err := store.ReserveBudget(e.Admin(), 3); err != nil {
		t.Fatal(err)
	}

	if err := store.ReleaseBudget(e.Admin(), yesterday); err != nil {
		t.Fatalf("ReleaseBudget: %v", err)
	}
	if n := e.WsCount(t, `
		SELECT coalesce(sum(enqueued), 0) FROM capture_auto_enrich_budget
		 WHERE budget_date = (now() AT TIME ZONE 'UTC')::date`); n != 1 {
		t.Fatalf("today's counter = %d, want 1 — a refund for yesterday must not free today's slot", n)
	}
}
