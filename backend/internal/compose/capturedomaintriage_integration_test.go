// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The triage-on-capture trigger's decisions.
//
// What these cover is the part that can go wrong quietly: which gates run, in
// which order, and — for every way the trigger gives up — whether the
// domain is left in a state the daily sweep still finds. That contract is the
// entire reason the trigger is allowed to be best-effort, so it is the thing
// worth holding.
//
// These held the enrich-on-capture trigger until capture stopped creating
// organizations; the gates, their order and the shared budget counter are the
// same, and the trigger they now guard is the one that still runs.
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
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/platform/database"
)

// openQuestion puts a domain in the state the ensure ladder leaves it: the
// company question recorded and unanswered, owned by a real human.
func openQuestion(t *testing.T, e *integration.Env, domain string) {
	t.Helper()
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			INSERT INTO organization_domain_disposition (workspace_id, domain, status, owner_id)
			VALUES ($1, $2, 'pending', $3)`, e.WS, domain, e.Rep1)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// stillDue reports whether the sweep would still pick this domain up. Every way
// the trigger gives up has to leave it true — that is the whole contract that
// lets the trigger be best-effort.
func stillDue(t *testing.T, e *integration.Env, domain string) bool {
	t.Helper()
	due, err := people.NewStore(e.Pool).ListDueDomains(e.Admin(), 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range due {
		if d.Domain == domain {
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

func TestTriageOnCaptureRespectsTheSettingBeforeSpendingAnything(t *testing.T) {
	e := integration.Setup(t)
	setAutoEnrich(t, e, false)
	openQuestion(t, e, "switched-off.example")

	trigger := newDomainTriageTrigger(e.Pool, slog.New(slog.DiscardHandler))
	trigger.domainPending(e.Admin(), "switched-off.example")

	if n := budgetSpent(t, e); n != 0 {
		t.Fatalf("%d budget slots spent with the setting off — the flag must be read before the budget", n)
	}
	if !stillDue(t, e, "switched-off.example") {
		t.Fatal("the domain was retired from the sweep by a trigger that did nothing")
	}
}

func TestTriageOnCaptureLeavesTheOrgToTheSweepAtTheDailyCap(t *testing.T) {
	e := integration.Setup(t)
	setAutoEnrich(t, e, true)
	openQuestion(t, e, "capped.example")

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

	trigger := newDomainTriageTrigger(e.Pool, slog.New(slog.DiscardHandler))
	trigger.dailyCap = testCap
	trigger.domainPending(e.Admin(), "capped.example")

	if n := budgetSpent(t, e); n != testCap {
		t.Fatalf("budget spent = %d, want the cap %d — the trigger must not spend past it", n, testCap)
	}
	if !stillDue(t, e, "capped.example") {
		t.Fatal("a capped trigger retired the organization — it must stay due for a later sweep")
	}
}

// Every way the trigger can give up has to leave the organization findable by
// the sweep. This drives the give-up that is hardest to reason about — the read
// itself failing to start — by running with no ambient River client, which is
// exactly what a missing queue looks like from here.
func TestTriageOnCaptureLeavesTheOrgToTheSweepWhenTheReadCannotStart(t *testing.T) {
	e := integration.Setup(t)
	setAutoEnrich(t, e, true)
	openQuestion(t, e, "no-queue.example")

	trigger := newDomainTriageTrigger(e.Pool, slog.New(slog.DiscardHandler))
	trigger.domainPending(e.Admin(), "no-queue.example")

	if !stillDue(t, e, "no-queue.example") {
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
func TestTriageOnCaptureIgnoresAnEmptyDomain(t *testing.T) {
	e := integration.Setup(t)
	setAutoEnrich(t, e, true)

	trigger := newDomainTriageTrigger(e.Pool, slog.New(slog.DiscardHandler))
	trigger.domainPending(e.Admin(), "")

	if n := budgetSpent(t, e); n != 0 {
		t.Fatalf("%d budget slots spent for a counterparty with no domain to read", n)
	}
}

// Reserve-before-spend means a caller sometimes holds a slot it turns out not to
// need — two paths racing on one domain both reserve, and the in-flight
// uniqueness index lets only one of them start a read. The refund is what keeps
// the day's allowance eroding a slot at a time, with the shortfall growing with
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
	if !today.Reserved {
		t.Fatal("setup reservation refused — the guard below would pass without ever running")
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
	if !todaySlot.Reserved {
		t.Fatal("setup reservation refused — yesterday's day would be the zero time")
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

// The refund has to survive the cancellation that can cause the failure it is
// compensating for. A capture cancelled mid-start owes a slot back and, without
// the detach, would try to return it on a context that is already dead —
// leaking the slot in precisely the case it was written to handle.
func TestAutoEnrichRefundSurvivesACancelledCaptureContext(t *testing.T) {
	e := integration.Setup(t)
	store := capture.NewAutoEnrichStore(e.Pool)
	slot, err := store.ReserveBudget(e.Admin(), 3)
	if err != nil {
		t.Fatal(err)
	}
	if !slot.Reserved {
		t.Fatal("setup reservation refused")
	}

	// The capture's context, already cancelled — what the trigger holds when a
	// sync is torn down mid-flight.
	cancelled, cancel := context.WithCancel(e.Admin())
	cancel()

	// Through the shared rule both the trigger and the sweep use. With no River
	// client the read cannot start, so this is the refund path — on a context
	// that is already dead.
	openQuestion(t, e, "cancelled.example")
	trigger := newDomainTriageTrigger(e.Pool, slog.New(slog.DiscardHandler))
	err = trigger.startOrRefund(cancelled, "cancelled.example", slot)
	if err == nil {
		t.Fatal("expected the start to fail on a cancelled context")
	}
	if n := budgetSpent(t, e); n != 0 {
		t.Fatalf("budget spent = %d, want 0 — the slot must come back even when the capture was cancelled", n)
	}
}

// dispositionOf reads a domain's standing verdict, or "" when it has none.
func dispositionOf(t *testing.T, e *integration.Env, domain string) string {
	t.Helper()
	var status string
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT coalesce(max(status), '') FROM organization_domain_disposition WHERE domain = $1`,
			domain).Scan(&status)
	}); err != nil {
		t.Fatal(err)
	}
	return status
}

// spendAttempts drives a domain to the end of its retry budget and makes it due,
// which is the state a site that never loads leaves behind.
func spendTriageAttempts(t *testing.T, e *integration.Env, domain string) {
	t.Helper()
	if err := database.WithWorkspaceTx(e.Admin(), e.Pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `
			UPDATE organization_domain_disposition
			   SET attempts = $2, next_attempt_at = now() - interval '1 day'
			 WHERE domain = $1`, domain, people.DomainTriageMaxAttempts)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// The sweep must CLOSE a question no crawl will ever answer. A domain that spent
// every attempt drops out of the due scan, so leaving it pending would strand
// its people without a company and with nothing on the row to say why — the
// person is created either way, and only the company waits on this.
func TestTriageSweepSettlesADomainThatRanOutOfAttempts(t *testing.T) {
	e := integration.Setup(t)
	setAutoEnrich(t, e, true)
	openQuestion(t, e, "unreachable.example")
	spendTriageAttempts(t, e, "unreachable.example")

	worker := newCaptureAutoEnrichSweepWorker(e.Pool, slog.New(slog.DiscardHandler))
	if err := worker.sweepDomainTriage(e.Admin(), 50); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	if got := dispositionOf(t, e, "unreachable.example"); got == "pending" || got == "" {
		t.Fatalf("disposition = %q after the sweep, want it settled", got)
	}
	if stillDue(t, e, "unreachable.example") {
		t.Fatal("a settled domain is still being offered for a crawl")
	}
}

// A domain with attempts left is left alone by the settle pass — settling it
// early would answer from no evidence a crawl could still have gathered.
func TestTriageSweepLeavesADomainThatStillHasAttempts(t *testing.T) {
	e := integration.Setup(t)
	setAutoEnrich(t, e, true)
	openQuestion(t, e, "waiting.example")

	worker := newCaptureAutoEnrichSweepWorker(e.Pool, slog.New(slog.DiscardHandler))
	// The enqueue needs an ambient River client this process has none of, so
	// the read cannot start and its budget slot is refunded. That is the path
	// under test: a sweep that cannot queue must leave the question open.
	if err := worker.sweepDomainTriage(e.Admin(), 50); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	if got := dispositionOf(t, e, "waiting.example"); got != "pending" {
		t.Fatalf("disposition = %q, want it still pending", got)
	}
	if n := budgetSpent(t, e); n != 0 {
		t.Fatalf("budget spent = %d, want 0 — a slot that bought no crawl must come back", n)
	}
}
