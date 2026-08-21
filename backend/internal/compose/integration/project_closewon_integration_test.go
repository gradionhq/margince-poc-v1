// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Winning a deal starts the delivery it was sold for.
//
// The project already exists when the deal is won — it has been accumulating
// since `initiative` — so the win is a transition, not a birth. These tests
// drive the REAL winning transition through the deals store and seed the
// project through the REAL project writer, because the whole claim is that the
// phase move commits with the win: a hand-written phase column would prove the
// column exists and nothing about the writer.
//
// The guards matter more than the happy path. A project carries several deals
// over years, phase movement is free-form in both directions, and a naive
// "won implies delivering" would re-open engagements somebody deliberately
// closed and restart work already under way.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// closeWonFixture is one project on one company, plus a deal pointing at it
// and the won stage that deal is advanced onto.
type closeWonFixture struct {
	project  ids.ProjectID
	deal     ids.DealID
	wonStage ids.StageID
}

// seedCloseWonFixture builds the fixture through the real writers: the real
// project store creates the project (so its birth history row is the writer's,
// not a fixture's), and the real deal store creates the deal pointing at it.
func seedCloseWonFixture(t *testing.T, e *Env, projectName string) closeWonFixture {
	t.Helper()
	pipeline, open, won := DealFixture(t, e)
	org := e.SeedOrg(t, "BAER Pharma", nil)
	p := seedProject(e.Admin(), t, e, projectName, nil, org, nil)

	orgID := orgIDOf(org)
	d, err := e.Deals.CreateDeal(e.Admin(), deals.CreateDealInput{
		Name: projectName + " phase one", PipelineID: pipeline, StageID: open,
		OrganizationID: &orgID, ProjectID: &p.ID, Source: "manual",
	})
	if err != nil {
		t.Fatalf("create the deal on the project: %v", err)
	}
	return closeWonFixture{
		project:  p.ID,
		deal:     ids.From[ids.DealKind](ids.UUID(d.Id)),
		wonStage: won,
	}
}

// deliveringHistoryCount counts the transitions recorded INTO delivering —
// the number that must not grow when a guard refuses to move the project.
func deliveringHistoryCount(t *testing.T, e *Env, project ids.ProjectID) int {
	t.Helper()
	return e.WsCount(t,
		`SELECT count(*) FROM project_phase_history WHERE project_id = $1 AND to_phase = $2`,
		project, deals.PhaseDelivering)
}

// phaseOf reads the project's phase back through the real read path.
func phaseOf(t *testing.T, e *Env, project ids.ProjectID) string {
	t.Helper()
	got, err := e.Deals.GetProject(e.Admin(), project, storekit.LiveOnly)
	if err != nil {
		t.Fatalf("read project %s: %v", project.UUID, err)
	}
	if got.Phase == nil {
		t.Fatalf("project %s came back with no phase", project.UUID)
	}
	return string(*got.Phase)
}

// The bridge itself: a deal won on a project that is being pursued moves that
// project into delivering, with the history row and the first-class event that
// every other phase move writes — because it goes through the same path.
func TestWinningADealStartsDeliveryOnItsProject(t *testing.T) {
	e := Setup(t)
	f := seedCloseWonFixture(t, e, "ERP replacement")

	// Pursuing is where a project sits while its deal is in flight, so that is
	// the state the win actually finds in the field.
	if _, err := e.Deals.AdvanceProjectPhase(e.Admin(), f.project, deals.AdvanceProjectPhaseInput{
		ToPhase: deals.PhasePursuing,
	}); err != nil {
		t.Fatalf("move the project to pursuing: %v", err)
	}

	if _, err := e.Deals.AdvanceDeal(e.Admin(), f.deal, wonInput(f.wonStage)); err != nil {
		t.Fatalf("win the deal: %v", err)
	}

	if got := phaseOf(t, e, f.project); got != deals.PhaseDelivering {
		t.Errorf("phase = %s after the win, want %s", got, deals.PhaseDelivering)
	}
	if n := e.WsCount(t,
		`SELECT count(*) FROM project_phase_history
		  WHERE project_id = $1 AND from_phase = $2 AND to_phase = $3`,
		f.project, deals.PhasePursuing, deals.PhaseDelivering); n != 1 {
		t.Errorf("pursuing→delivering history rows = %d, want exactly 1", n)
	}
	// The event is asserted on its payload, not merely on its type: the
	// pursuing move in the setup published one too, so a type-only count would
	// pass on the wrong event.
	if n := e.WsCount(t,
		`SELECT count(*) FROM event_outbox
		  WHERE envelope->>'type' = 'project.phase_changed'
		    AND envelope->'entity'->>'id' = $1::text
		    AND envelope->'payload'->>'from_phase' = $2
		    AND envelope->'payload'->>'to_phase' = $3`,
		f.project, deals.PhasePursuing, deals.PhaseDelivering); n != 1 {
		t.Errorf("pursuing→delivering events = %d, want exactly 1", n)
	}
	// The audit row is the other half of the write shape, and the transition
	// must carry the same action a human-driven advance carries — a reader
	// filtering the log for phase moves must find this one.
	if n := e.WsCount(t,
		`SELECT count(*) FROM audit_log
		  WHERE entity_type = 'project' AND entity_id = $1 AND action = 'advance_phase'
		    AND after->>'phase' = $2`,
		f.project, deals.PhaseDelivering); n != 1 {
		t.Errorf("advance_phase audit rows into delivering = %d, want exactly 1", n)
	}
}

// A project still at the head of the ladder when its deal lands moves too:
// initiative is where a project is born, and a deal can be won off one that
// never passed through pursuing.
func TestWinningADealStartsDeliveryFromInitiativeToo(t *testing.T) {
	e := Setup(t)
	f := seedCloseWonFixture(t, e, "Validation")

	if got := phaseOf(t, e, f.project); got != deals.PhaseInitiative {
		t.Fatalf("the fixture project starts at %s, want %s", got, deals.PhaseInitiative)
	}
	if _, err := e.Deals.AdvanceDeal(e.Admin(), f.deal, wonInput(f.wonStage)); err != nil {
		t.Fatalf("win the deal: %v", err)
	}
	if got := phaseOf(t, e, f.project); got != deals.PhaseDelivering {
		t.Errorf("phase = %s after the win, want %s", got, deals.PhaseDelivering)
	}
}

// A second deal landing on work already under way is not a transition. Writing
// one would claim a restart that never happened, and every "when did delivery
// begin" answer derived from the history would move to the later date.
func TestWinningADealOnAnAlreadyDeliveringProjectWritesNothing(t *testing.T) {
	e := Setup(t)
	f := seedCloseWonFixture(t, e, "Rollout")

	if _, err := e.Deals.AdvanceProjectPhase(e.Admin(), f.project, deals.AdvanceProjectPhaseInput{
		ToPhase: deals.PhaseDelivering,
	}); err != nil {
		t.Fatalf("move the project to delivering: %v", err)
	}
	before := deliveringHistoryCount(t, e, f.project)
	if before != 1 {
		t.Fatalf("the setup wrote %d delivering rows, want 1", before)
	}

	if _, err := e.Deals.AdvanceDeal(e.Admin(), f.deal, wonInput(f.wonStage)); err != nil {
		t.Fatalf("win the deal: %v", err)
	}

	if got := deliveringHistoryCount(t, e, f.project); got != before {
		t.Errorf("delivering history rows = %d after the win, want %d — a no-op writes nothing", got, before)
	}
	if got := phaseOf(t, e, f.project); got != deals.PhaseDelivering {
		t.Errorf("phase = %s, want it left at %s", got, deals.PhaseDelivering)
	}
}

// The guard that matters most: a renewal closing in year three must NOT
// silently re-open an engagement somebody deliberately ended with a reason.
// Re-opening is a human decision made with that reason in hand, and this path
// has none to offer.
func TestWinningADealDoesNotReopenAClosedProject(t *testing.T) {
	e := Setup(t)
	f := seedCloseWonFixture(t, e, "Support retainer")

	reason := "Delivered and signed off."
	if _, err := e.Deals.AdvanceProjectPhase(e.Admin(), f.project, deals.AdvanceProjectPhaseInput{
		ToPhase: deals.PhaseClosed, Reason: &reason,
	}); err != nil {
		t.Fatalf("close the project: %v", err)
	}

	if _, err := e.Deals.AdvanceDeal(e.Admin(), f.deal, wonInput(f.wonStage)); err != nil {
		t.Fatalf("winning a renewal on a closed project must still succeed: %v", err)
	}

	if got := phaseOf(t, e, f.project); got != deals.PhaseClosed {
		t.Errorf("phase = %s after the renewal win, want it left %s", got, deals.PhaseClosed)
	}
	if n := deliveringHistoryCount(t, e, f.project); n != 0 {
		t.Errorf("delivering history rows = %d, want 0 — the close was deliberate", n)
	}
	// And the close's own explanation survives: a re-open through the ordinary
	// path clears it, so a lingering reason would be evidence the guard leaked.
	got, err := e.Deals.GetProject(e.Admin(), f.project, storekit.LiveOnly)
	if err != nil {
		t.Fatal(err)
	}
	if got.ClosedReason == nil || *got.ClosedReason != reason {
		t.Errorf("closed_reason = %v, want it untouched", got.ClosedReason)
	}
}

// A deal with no project has nothing to advance. Creating one, and guessing
// which existing project a projectless deal meant, are separate questions.
func TestWinningAProjectlessDealTouchesNoProject(t *testing.T) {
	e := Setup(t)
	pipeline, open, won := DealFixture(t, e)
	org := e.SeedOrg(t, "BAER Pharma", nil)
	// A live project on the same company that the win must not reach for.
	bystander := seedProject(e.Admin(), t, e, "Unrelated work", nil, org, nil)

	orgID := orgIDOf(org)
	d, err := e.Deals.CreateDeal(e.Admin(), deals.CreateDealInput{
		Name: "No project", PipelineID: pipeline, StageID: open,
		OrganizationID: &orgID, Source: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := e.Deals.AdvanceDeal(e.Admin(), ids.From[ids.DealKind](ids.UUID(d.Id)), wonInput(won)); err != nil {
		t.Fatalf("win the projectless deal: %v", err)
	}

	if got := phaseOf(t, e, bystander.ID); got != deals.PhaseInitiative {
		t.Errorf("the bystander project moved to %s — a projectless win reached for it", got)
	}
	if n := e.WsCount(t,
		`SELECT count(*) FROM project_phase_history WHERE to_phase = $1`, deals.PhaseDelivering); n != 0 {
		t.Errorf("delivering history rows = %d workspace-wide, want 0", n)
	}
}

// Re-winning a deal already sitting on its won stage runs the win branch
// again. The project is delivering by then, so the phase guard must absorb it:
// a second transition would record a restart nobody performed.
func TestReWinningADealWritesNoSecondTransition(t *testing.T) {
	e := Setup(t)
	f := seedCloseWonFixture(t, e, "Migration")

	for round := 1; round <= 2; round++ {
		if _, err := e.Deals.AdvanceDeal(e.Admin(), f.deal, wonInput(f.wonStage)); err != nil {
			t.Fatalf("win round %d: %v", round, err)
		}
	}

	if n := deliveringHistoryCount(t, e, f.project); n != 1 {
		t.Errorf("delivering history rows = %d after two wins, want exactly 1", n)
	}
	if n := e.WsCount(t,
		`SELECT count(*) FROM event_outbox
		  WHERE envelope->>'type' = 'project.phase_changed' AND envelope->'entity'->>'id' = $1::text`,
		f.project); n != 1 {
		t.Errorf("project.phase_changed events = %d after two wins, want exactly 1", n)
	}
}

// Re-asserting the won stage must not drive the project anywhere. The bridge
// is gated on the deal actually BECOMING won, not on the status it ends up
// with — otherwise anyone who can advance the deal can force the project back
// to delivering after a project-authorized human moved it on, repeatedly, and
// without needing to see the project at all.
//
// The previous replay test cannot catch this: it never moves the project away
// from delivering between the two wins, so a bridge that re-ran would write
// nothing either way and pass.
func TestReAssertingAWonStageDoesNotDriveTheProjectBack(t *testing.T) {
	e := Setup(t)
	f := seedCloseWonFixture(t, e, "Data platform")

	if _, err := e.Deals.AdvanceDeal(e.Admin(), f.deal, wonInput(f.wonStage)); err != nil {
		t.Fatalf("win the deal: %v", err)
	}
	if got := phaseOf(t, e, f.project); got != deals.PhaseDelivering {
		t.Fatalf("phase = %s after the win, want %s", got, deals.PhaseDelivering)
	}

	// Somebody with authority over the project deliberately moves it back —
	// the scope was cut, delivery has not started after all.
	if _, err := e.Deals.AdvanceProjectPhase(e.Admin(), f.project, deals.AdvanceProjectPhaseInput{
		ToPhase: deals.PhasePursuing,
	}); err != nil {
		t.Fatalf("move the project back to pursuing: %v", err)
	}

	// The deal owner re-asserts the stage the deal is already on. Nothing
	// about the deal changes, so nothing about the project may change either.
	if _, err := e.Deals.AdvanceDeal(e.Admin(), f.deal, wonInput(f.wonStage)); err != nil {
		t.Fatalf("re-assert the won stage: %v", err)
	}

	if got := phaseOf(t, e, f.project); got != deals.PhasePursuing {
		t.Errorf("phase = %s — re-asserting the win overrode a deliberate move to %s",
			got, deals.PhasePursuing)
	}
	if n := deliveringHistoryCount(t, e, f.project); n != 1 {
		t.Errorf("delivering history rows = %d, want the 1 the real win wrote", n)
	}
}

// A project carries several deals over years. When two of them win, the first
// starts delivery and the second finds work already under way — one
// transition, not two. Sequential wins are the case a user actually hits; the
// concurrent shape is held by the row lock the bridge takes before it decides.
func TestTwoDealsWinningOnOneProjectProduceOneTransition(t *testing.T) {
	e := Setup(t)
	pipeline, open, won := DealFixture(t, e)
	org := e.SeedOrg(t, "BAER Pharma", nil)
	p := seedProject(e.Admin(), t, e, "Multi-phase programme", nil, org, nil)

	orgID := orgIDOf(org)
	var wonDeals []ids.DealID
	for _, name := range []string{"Phase one", "Phase two"} {
		d, err := e.Deals.CreateDeal(e.Admin(), deals.CreateDealInput{
			Name: name, PipelineID: pipeline, StageID: open,
			OrganizationID: &orgID, ProjectID: &p.ID, Source: "manual",
		})
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		wonDeals = append(wonDeals, ids.From[ids.DealKind](ids.UUID(d.Id)))
	}
	for i, deal := range wonDeals {
		if _, err := e.Deals.AdvanceDeal(e.Admin(), deal, wonInput(won)); err != nil {
			t.Fatalf("win deal %d: %v", i+1, err)
		}
	}

	if n := deliveringHistoryCount(t, e, p.ID); n != 1 {
		t.Errorf("delivering history rows = %d after two deals won, want exactly 1", n)
	}
	if n := e.WsCount(t,
		`SELECT count(*) FROM event_outbox
		  WHERE envelope->>'type' = 'project.phase_changed' AND envelope->'entity'->>'id' = $1::text`,
		p.ID); n != 1 {
		t.Errorf("project.phase_changed events = %d, want exactly 1", n)
	}
}

// Winning a deal whose project was archived is a no-op, not a failure. Failing
// would roll back the win itself over somebody else's archive — a deal that
// cannot be closed because an unrelated grouping was tidied away.
func TestWinningADealWhoseProjectWasArchivedStillWins(t *testing.T) {
	e := Setup(t)
	f := seedCloseWonFixture(t, e, "Retired programme")

	if _, err := e.Deals.ArchiveProject(e.Admin(), f.project, nil); err != nil {
		t.Fatalf("archive the project: %v", err)
	}

	if _, err := e.Deals.AdvanceDeal(e.Admin(), f.deal, wonInput(f.wonStage)); err != nil {
		t.Fatalf("the win was rolled back by an archived project: %v", err)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM deal WHERE id = $1 AND status = 'won'`, f.deal); n != 1 {
		t.Error("the deal did not end up won")
	}
	if n := deliveringHistoryCount(t, e, f.project); n != 0 {
		t.Errorf("delivering history rows = %d, want 0 — an archived project is not resurrected", n)
	}
}

// waitForBlockedOnProject blocks until some other backend is WAITING on a row
// lock held against the project — the observable proof that the win reached
// LockRow and parked, rather than the test merely getting there first.
//
// It asks Postgres for the fact instead of sleeping: pg_locks names the
// waiter, so there is no timing assumption to tune. Failing here is the signal
// the race test exists to give — a bridge that takes no lock never parks, so
// the wait times out and the test fails rather than silently proving nothing.
func waitForBlockedOn(ctx context.Context, t *testing.T, probe pgx.Tx, holderPID int) {
	t.Helper()
	// Paced with a ticker rather than spun: this loop and the win it watches
	// compete for the same processor, and a tight loop is one of the ways a
	// loaded runner starves the very writer whose progress it waits on.
	pace := time.NewTicker(25 * time.Millisecond)
	defer pace.Stop()
	budget, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	for {
		blocked, err := blockedBy(budget, probe, holderPID)
		if err != nil && budget.Err() == nil {
			t.Fatalf("probing for a backend blocked on the project row: %v", err)
		}
		if blocked {
			return
		}
		select {
		case <-pace.C:
		case <-budget.Done():
			t.Fatal("no backend ever blocked on the held project row — the delivery bridge " +
				"decided its transition without taking the lock, so this run proves nothing")
		}
	}
}

// blockedBy asks whether any backend in this database is waiting on the
// holder. pg_blocking_pids answers the question directly, so nothing here has
// to reason about which lock shape Postgres reports mid-queue.
//
// The snapshot is cleared first because pg_stat_activity is materialized once
// per transaction and cached until it ends: a probe running inside the holder's
// long transaction would otherwise keep answering from the view it had before
// the win's connection existed, and never see the waiter at all.
func blockedBy(ctx context.Context, probe pgx.Tx, holderPID int) (bool, error) {
	if _, err := probe.Exec(ctx, `SELECT pg_stat_clear_snapshot()`); err != nil {
		return false, err
	}
	var blocked bool
	err := probe.QueryRow(ctx, `
		SELECT EXISTS (
		  SELECT 1 FROM pg_stat_activity a
		   WHERE a.datname = current_database() AND $1 = ANY (pg_blocking_pids(a.pid)))`,
		holderPID).Scan(&blocked)
	return blocked, err
}

// The race the row lock exists to close, forced rather than hoped for.
//
// A sequential test cannot see this: swap the lock for a plain read and every
// other test in this file still passes. So this one holds the project row from
// a second transaction, starts the win (which must block on the lock rather
// than reading a stale phase), closes the project, and commits. The win then
// acquires the lock and must read the CLOSED phase — not the `pursuing` it
// would have seen had it decided before locking.
//
// Without the lock the win reads pursuing, writes phase=delivering over the
// committed close, and — because its stale snapshot carried no closed_reason —
// never clears that column, leaving a delivering project that still explains
// why it was closed.
func TestTheDeliveryTransitionDecidesUnderTheProjectLock(t *testing.T) {
	e := Setup(t)
	f := seedCloseWonFixture(t, e, "Contended programme")
	if _, err := e.Deals.AdvanceProjectPhase(e.Admin(), f.project, deals.AdvanceProjectPhaseInput{
		ToPhase: deals.PhasePursuing,
	}); err != nil {
		t.Fatalf("move the project to pursuing: %v", err)
	}

	// Held from its own transaction, so the win below cannot pass LockRow
	// until this one commits. No sleep decides the ordering — the lock does.
	holderCtx := principal.WithWorkspaceID(context.Background(), e.WS)
	holder, err := e.Pool.Begin(holderCtx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := holder.Exec(holderCtx,
		`SELECT set_config('app.workspace_id', $1::text, true)`, e.WS); err != nil {
		t.Fatal(err)
	}
	var holderPID int
	if err := holder.QueryRow(holderCtx,
		`SELECT id, pg_backend_pid() FROM project WHERE id = $1 FOR UPDATE`,
		f.project).Scan(new(ids.UUID), &holderPID); err != nil {
		t.Fatal(err)
	}

	winErr := make(chan error, 1)
	go func() {
		_, err := e.Deals.AdvanceDeal(e.Admin(), f.deal, wonInput(f.wonStage))
		winErr <- err
	}()

	// Wait until the win is genuinely PARKED behind this holder, asked of
	// Postgres rather than assumed after a sleep. Without this the close can
	// commit before the win has even opened its transaction, and the test
	// would pass against a bridge that holds no lock at all.
	waitForBlockedOn(holderCtx, t, holder, holderPID)

	// The close commits while the win is parked on the lock, so the phase the
	// win eventually reads is one that did not exist when it started.
	closeReason := "Cancelled before delivery."
	if _, err := holder.Exec(holderCtx,
		`UPDATE project SET phase = $2, closed_reason = $3, version = version + 1 WHERE id = $1`,
		f.project, deals.PhaseClosed, closeReason); err != nil {
		t.Fatal(err)
	}
	if err := holder.Commit(holderCtx); err != nil {
		t.Fatal(err)
	}

	if err := <-winErr; err != nil {
		t.Fatalf("the win failed against a concurrently closed project: %v", err)
	}

	if got := phaseOf(t, e, f.project); got != deals.PhaseClosed {
		t.Errorf("phase = %s — the win decided on a phase read before it held the lock", got)
	}
	if n := deliveringHistoryCount(t, e, f.project); n != 0 {
		t.Errorf("delivering history rows = %d, want 0 — the close won the race", n)
	}
	got, err := e.Deals.GetProject(e.Admin(), f.project, storekit.LiveOnly)
	if err != nil {
		t.Fatal(err)
	}
	if got.ClosedReason == nil || *got.ClosedReason != closeReason {
		t.Errorf("closed_reason = %v, want the concurrent close's reason intact", got.ClosedReason)
	}
}

// The audit row must not claim an authorization that never happened. This path
// is admitted by deal.update and deliberately skips the project's row scope,
// but audit_log.authorization_rule is derived from the entity and action and
// so always reads `project.update`. The evidence column is what makes the
// ledger honest, and its absence is exactly the silent overclaim.
func TestTheDeliveryTransitionRecordsWhatActuallyAuthorizedIt(t *testing.T) {
	e := Setup(t)
	f := seedCloseWonFixture(t, e, "Attribution")

	if _, err := e.Deals.AdvanceDeal(e.Admin(), f.deal, wonInput(f.wonStage)); err != nil {
		t.Fatalf("win the deal: %v", err)
	}

	if n := e.WsCount(t,
		`SELECT count(*) FROM audit_log
		  WHERE entity_type = 'project' AND entity_id = $1 AND action = 'advance_phase'
		    AND evidence->>'authorized_by' = 'deal.update'
		    AND evidence->>'project_row_scope' = 'not_checked'
		    AND evidence->>'transition_trigger' = 'deal_won'`,
		f.project); n != 1 {
		t.Errorf("delivery audit rows naming their real authority = %d, want 1", n)
	}

	// And a human-driven advance, which DID check project.update, carries no
	// such evidence — the marker means something only if it is not on every row.
	if _, err := e.Deals.AdvanceProjectPhase(e.Admin(), f.project, deals.AdvanceProjectPhaseInput{
		ToPhase: deals.PhaseClosed, Reason: strPtr("Delivered."),
	}); err != nil {
		t.Fatal(err)
	}
	if n := e.WsCount(t,
		`SELECT count(*) FROM audit_log
		  WHERE entity_type = 'project' AND entity_id = $1 AND action = 'advance_phase'
		    AND after->>'phase' = $2 AND evidence->'authorized_by' IS NOT NULL`,
		f.project, deals.PhaseClosed); n != 0 {
		t.Errorf("human-driven advances carrying the cross-scope marker = %d, want 0", n)
	}
}
