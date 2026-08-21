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
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
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
