// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// What approving an automation's staged action actually does.
//
// Rejecting one has always terminated its run. Approving one did nothing at
// all: the effect never ran, and the run stayed in requires_approval forever,
// so a human who pressed Approve got a card that vanished and a firing that
// still reads as waiting for them.
//
// Underneath that was a worse one. Two of the kinds an automation can stage had
// no decision-grant mapping, and requireDecisionGrants fails closed — so those
// stagings were hidden from every inbox and could not be decided by ANYBODY.
// The census gates in approval_kinds_test.go hold the registries to each other;
// these run the whole path against a real database, because a mapping can be
// present and the inbox still not show the card.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/automation"
	kevents "github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

// parkedRun stages one approval of the given kind against a target and parks a
// workflow run behind it, exactly as ApplyActions does when a firing stages.
//
// The run row is written directly because the point under test is what a
// DECISION does to a parked run; driving a real firing to produce one would
// require an automation that plans the kind, which for a scaled reassignment
// needs a scale signal no shipped template carries. The linkage that matters is
// reproduced faithfully — detail->>'approval_id', the field the transitions
// match on — so a change to that contract fails here.
func parkedRun(t *testing.T, e *integration.Env, svc *approvals.Service, kind string,
	targetID ids.UUID, patch string,
) (ids.ApprovalID, string) {
	t.Helper()
	id, err := svc.Stage(e.Admin(), approvals.StageInput{
		Kind:           kind,
		ProposedChange: json.RawMessage(patch),
		DiffHash:       "approvedrun-" + ids.NewV7().String(),
		Summary:        "automation wants to " + kind,
		TargetType:     "person",
		TargetID:       targetID,
	})
	if err != nil {
		t.Fatalf("staging %s: %v", kind, err)
	}
	handler := "wf_" + ids.NewV7().String()
	e.WsExec(t, `
		INSERT INTO workflow_run (handler, idempotency_key, trigger_event, planned, status, detail)
		VALUES ($1, $2, $3, '[]'::jsonb, 'requires_approval', jsonb_build_object('approval_id', $4::text))`,
		handler, handler+":1", ids.NewV7(), id.String())
	return id, handler
}

func runStatus(t *testing.T, e *integration.Env, handler string) string {
	t.Helper()
	var status string
	if err := e.Pool.QueryRow(context.Background(),
		`SELECT status FROM workflow_run WHERE handler = $1`, handler).Scan(&status); err != nil {
		t.Fatalf("reading the run: %v", err)
	}
	return status
}

// The defect at the level a human meets it: the inbox. A census can agree that
// a grant exists while the listing still hides the card, so this asks the
// service the HTTP surface asks.
func TestAnAutomationStagedActionIsVisibleAndDecidable(t *testing.T) {
	e := integration.Setup(t)
	svc := approvalsServiceWithEffects(e.Pool)
	person := e.SeedPerson(t, "Reassignment Target", nil)

	for _, kind := range automation.StageableKinds() {
		t.Run(kind, func(t *testing.T) {
			id, _ := parkedRun(t, e, svc, kind, person, `{"owner_id":null}`)
			if _, err := svc.Get(decider(e), id); err != nil {
				t.Fatalf("an admin cannot even read the staging: %v — it is hidden from the inbox and nobody can decide it", err)
			}
		})
	}
}

// The title case of the issue, for the one kind whose whole effect is the
// asking: request_approval's yes is the outcome, so the run finishes.
func TestApprovingAnAskingOnlyStagingCompletesItsRun(t *testing.T) {
	e := integration.Setup(t)
	svc := approvalsServiceWithEffects(e.Pool)
	person := e.SeedPerson(t, "Asked About", nil)
	kind := string(workflow.ActionEmitFlowEvent)
	id, handler := parkedRun(t, e, svc, kind, person, `{"note":"needs a human"}`)

	if _, err := svc.Decide(decider(e), id, true, nil); err != nil {
		t.Fatalf("Decide(approve) → %v", err)
	}
	engine := NewWorkflowEngine(e.DB())
	if err := engine.HandleApprovalDecided(context.Background(), decidedEnvelope(t, id, "approved", kind)); err != nil {
		t.Fatalf("HandleApprovalDecided → %v", err)
	}
	if got := runStatus(t, e, handler); got != "applied" {
		t.Errorf("run is %q after its approval, want applied — a human answered and the firing still reads as waiting for them", got)
	}
}

// A refusal must still block, and a redelivered decision must not double-write.
// The consumer's two arms share one entry point, so the arm that was there
// first is asserted beside the arm that was added.
func TestRefusingAnAskingOnlyStagingStillBlocksItsRun(t *testing.T) {
	e := integration.Setup(t)
	svc := approvalsServiceWithEffects(e.Pool)
	person := e.SeedPerson(t, "Asked About", nil)
	kind := string(workflow.ActionEmitFlowEvent)
	id, handler := parkedRun(t, e, svc, kind, person, `{"note":"needs a human"}`)

	if _, err := svc.Decide(decider(e), id, false, nil); err != nil {
		t.Fatalf("Decide(reject) → %v", err)
	}
	engine := NewWorkflowEngine(e.DB())
	env := decidedEnvelope(t, id, "rejected", kind)
	if err := engine.HandleApprovalDecided(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	if got := runStatus(t, e, handler); got != "blocked" {
		t.Fatalf("run is %q after a refusal, want blocked", got)
	}
	// Redelivery: the bus is at-least-once, and the predicate is what keeps a
	// second copy from moving a run that has already finished.
	if err := engine.HandleApprovalDecided(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	if got := runStatus(t, e, handler); got != "blocked" {
		t.Errorf("a redelivered refusal moved the run to %q", got)
	}
}

// A run that reached a terminal outcome must not be reopened by a late
// approved event — the one ordering the predicate exists to refuse.
func TestACompletionDoesNotReviveABlockedRun(t *testing.T) {
	e := integration.Setup(t)
	svc := approvalsServiceWithEffects(e.Pool)
	person := e.SeedPerson(t, "Asked About", nil)
	kind := string(workflow.ActionEmitFlowEvent)
	id, handler := parkedRun(t, e, svc, kind, person, `{"note":"needs a human"}`)
	engine := NewWorkflowEngine(e.DB())

	if err := engine.HandleApprovalDecided(context.Background(), decidedEnvelope(t, id, "rejected", kind)); err != nil {
		t.Fatal(err)
	}
	if err := engine.HandleApprovalDecided(context.Background(), decidedEnvelope(t, id, "approved", kind)); err != nil {
		t.Fatal(err)
	}
	if got := runStatus(t, e, handler); got != "blocked" {
		t.Errorf("a late approved event moved a blocked run to %q — a refused firing now reads as applied", got)
	}
}

// decidedEnvelope builds the approval.decided event the relay would deliver.
//
// The kind is a PARAMETER and not a constant, because the consumer branches on
// it: hardcoding the asking-only kind made the per-kind loop below hand every
// kind an envelope claiming to be asking-only, so the consumer completed each
// run itself and the loop passed whether or not the kind's own executor did —
// which is the exact failure that loop exists to detect.
func decidedEnvelope(t *testing.T, id ids.ApprovalID, verdict, kind string) kevents.Envelope {
	t.Helper()
	payload, err := json.Marshal(map[string]string{
		"verdict": verdict, "kind": kind,
	})
	if err != nil {
		t.Fatal(err)
	}
	return kevents.Envelope{
		EventID: ids.NewV7(),
		Type:    "approval.decided",
		Entity:  kevents.EntityRef{Type: "approval", ID: id.UUID},
		Payload: payload,
	}
}

// The write half, end to end: approving a reassignment at scale must actually
// move the owner, spend the card, and finish the run.
//
// Before this the effect did not exist. The decision committed, the inbox
// emptied, and the person kept the owner they had — a human's authorization
// spent on nothing, with the run still reading as waiting for it.
func TestApprovingAReassignmentMovesTheOwnerAndCompletesItsRun(t *testing.T) {
	e := integration.Setup(t)
	svc := approvalsServiceWithEffects(e.Pool)
	newOwner := e.Rep1
	person := e.SeedPerson(t, "Reassigned At Scale", nil)
	id, handler := parkedRun(t, e, svc, string(workflow.ActionAssignOwner), person,
		`{"owner_id":"`+newOwner.String()+`"}`)

	if _, err := svc.Decide(decider(e), id, true, nil); err != nil {
		t.Fatalf("Decide(approve) → %v", err)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM person WHERE id = $1 AND owner_id = $2`, person, newOwner); n != 1 {
		t.Error("the person still has their old owner — the approved reassignment ran nothing")
	}
	if got := runStatus(t, e, handler); got != "applied" {
		t.Errorf("run is %q after the reassignment it staged was approved and performed, want applied", got)
	}
	// Single-use: the redemption consumes the card, so a second decision on the
	// same approval is refused whatever happened to the write after it.
	if _, err := svc.Decide(decider(e), id, true, nil); err == nil {
		t.Error("an already-released reassignment was approvable again")
	}
}

// The ordering argument, stated as a test: a write that FAILS must not leave a
// run claiming the work happened.
//
// The approval is spent either way — redeem-then-execute is the discipline every
// 🟡 executor here follows, and the version pin makes the reverse order
// impossible (the write would bump the version its own redemption checks). What
// the ordering does buy is an honest run: the transition runs only after the
// provider returns, so a failed reassignment leaves its firing parked rather
// than reporting an owner change that never happened.
func TestAFailedReassignmentDoesNotMarkItsRunApplied(t *testing.T) {
	e := integration.Setup(t)
	svc := approvalsServiceWithEffects(e.Pool)
	person := e.SeedPerson(t, "Reassignment Fails", nil)
	// The create stamps the seeding seat as owner; the test wants an unowned
	// record so the failed write's footprint is unmistakable.
	e.WsExec(t, `UPDATE person SET owner_id = NULL WHERE id = $1`, person)
	// An owner that does not exist: the store's foreign key refuses it, which is
	// an ordinary failure of the write rather than a fault injected around it.
	id, handler := parkedRun(t, e, svc, string(workflow.ActionAssignOwner), person,
		`{"owner_id":"`+ids.NewV7().String()+`"}`)

	if _, err := svc.Decide(decider(e), id, true, nil); err == nil {
		t.Fatal("a reassignment onto a non-existent owner reported success")
	}
	if got := runStatus(t, e, handler); got != "requires_approval" {
		t.Errorf("run is %q after its write failed, want requires_approval — a firing whose work never happened reads as finished", got)
	}
	if n := e.WsCount(t, `SELECT count(*) FROM person WHERE id = $1 AND owner_id IS NOT NULL`, person); n != 0 {
		t.Error("the failed reassignment left an owner on the record")
	}
}

// Approving EVERY stageable kind must leave its run terminal.
//
// The census in approval_kinds_test.go proves a release executor is registered.
// Registration is not execution: an executor that performs its write and forgets
// CompleteApprovedRunTx satisfies that census and still strands its run in
// requires_approval, which is the defect one layer in. This runs the real
// decision against a real database for each kind and asserts the run stopped
// waiting, so the next kind added is held to the outcome rather than the wiring.
//
// The send-path kinds (lateApprovalEffects) are not registered on this service
// and have their own suite; skipping them here is scoped, not silent — the loop
// says which kinds it covered.
func TestApprovingAnyStageableKindLeavesItsRunTerminal(t *testing.T) {
	e := integration.Setup(t)
	svc := approvalsServiceWithEffects(e.Pool)
	registered := map[string]bool{}
	for _, kind := range svc.EffectKinds() {
		registered[kind] = true
	}
	for _, kind := range automation.AskingOnlyKinds() {
		registered[kind] = true
	}

	covered := 0
	for _, kind := range automation.StageableKinds() {
		if !registered[kind] {
			t.Logf("skipping %q: registered late with the send path, covered by its own suite", kind)
			continue
		}
		t.Run(kind, func(t *testing.T) {
			person := e.SeedPerson(t, "Terminal Run "+kind, nil)
			id, handler := parkedRun(t, e, svc, kind, person,
				`{"owner_id":"`+e.Rep1.String()+`"}`)

			if _, err := svc.Decide(decider(e), id, true, nil); err != nil {
				t.Fatalf("Decide(approve) → %v", err)
			}
			// Asking-only kinds finish through the decision consumer, which the
			// relay drives; write-proposing kinds finish inside their executor.
			if err := NewWorkflowEngine(e.DB()).HandleApprovalDecided(
				context.Background(), decidedEnvelope(t, id, "approved", kind)); err != nil {
				t.Fatal(err)
			}
			if got := runStatus(t, e, handler); got == "requires_approval" {
				t.Errorf("run behind an approved %q staging is still waiting — its executor ran but never completed the run", kind)
			}
		})
		covered++
	}
	if covered == 0 {
		t.Fatal("no stageable kind was covered — the scan found nothing to check, which means it is broken")
	}
}
