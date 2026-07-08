// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package agents

// Honest run recording (B-E15.3a + A72/ADR-0035 Am.1) over a real
// migrated Postgres: every firing outcome — applied, skipped, failed at
// Match/Plan/Apply, staged — lands durably with its reason, at-least-once
// redelivery grows nothing, and a rejected staging flips the parked run
// to its terminal 'blocked' outcome naming the approval.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	kevents "github.com/gradionhq/margince/backend/internal/shared/kernel/events"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/workflow"
)

func TestFiringPathRecordsEveryTerminalOutcomeWithItsReason(t *testing.T) {
	fx := setupAutomationDB(t)
	stagedApproval := ids.New[ids.ApprovalKind]()

	handlers := []scriptedWorkflow{
		{name: "wf_ok"},
		{name: "wf_skip", match: func(workflow.Event) (bool, error) { return false, nil }},
		{name: "wf_matchfail", match: func(workflow.Event) (bool, error) { return false, errors.New("boom-match") }},
		{name: "wf_planfail", plan: func(workflow.Event) (workflow.Effect, error) { return workflow.Effect{}, errors.New("boom-plan") }},
		{name: "wf_applyfail", apply: func(workflow.Event) (workflow.RunResult, error) {
			return workflow.RunResult{}, errors.New("boom-apply")
		}},
		{name: "wf_staged", apply: func(workflow.Event) (workflow.RunResult, error) {
			return workflow.RunResult{}, &StagedApprovalError{ApprovalID: stagedApproval}
		}},
	}
	engine := NewWorkflowEngine(fx.pool)
	for _, h := range handlers {
		fx.seedAutomation(t, h.name)
		engine.RegisterWorkflow(h)
	}

	env := kevents.Envelope{
		EventID:     ids.NewV7(),
		Type:        scriptedTrigger,
		WorkspaceID: fx.ws,
		OccurredAt:  time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC),
		Entity:      kevents.EntityRef{Type: "deal", ID: ids.NewV7()},
	}
	if err := engine.HandleEvent(context.Background(), env); err == nil {
		t.Fatal("HandleEvent swallowed the handler failures — the dispatcher must still surface them")
	}
	assertRecordedOutcomes(t, fx, len(handlers), stagedApproval)

	// At-least-once redelivery: the claims absorb the duplicate — same
	// rows, same statuses, and the failures still surface.
	if err := engine.HandleEvent(context.Background(), env); err == nil {
		t.Fatal("redelivered HandleEvent swallowed the handler failures")
	}
	if again := fx.runsByHandler(t); len(again) != len(handlers) {
		t.Fatalf("redelivery grew the run history to %d rows, want %d", len(again), len(handlers))
	}

	assertRejectionBlocksParkedRun(t, fx, engine, stagedApproval)
}

// assertRecordedOutcomes checks each scripted handler landed exactly one
// run row in its terminal state, reason on the error column.
func assertRecordedOutcomes(t *testing.T, fx *autoFixture, wantRuns int, stagedApproval ids.ApprovalID) {
	t.Helper()
	runs := fx.runsByHandler(t)
	if len(runs) != wantRuns {
		t.Fatalf("recorded %d runs, want one per handler (%d) — an outcome went unrecorded", len(runs), wantRuns)
	}
	detail := func(name string) string {
		run := runs[name]
		if run.detail == nil {
			t.Fatalf("%s: run has no reason on the error column", name)
		}
		return *run.detail
	}
	if run := runs["wf_ok"]; run.status != "applied" || run.detail != nil {
		t.Errorf("wf_ok = %+v, want a clean applied run", run)
	}
	if run := runs["wf_skip"]; run.status != "skipped" || run.planned != "[]" {
		t.Errorf("wf_skip = %+v, want skipped with an empty plan", run)
	} else if !strings.Contains(detail("wf_skip"), "did not satisfy") {
		t.Errorf("wf_skip reason %q does not say why nothing happened", detail("wf_skip"))
	}
	if run := runs["wf_matchfail"]; run.status != "failed" || !strings.Contains(detail("wf_matchfail"), "boom-match") {
		t.Errorf("wf_matchfail = %+v, want failed carrying the match error", run)
	}
	if run := runs["wf_planfail"]; run.status != "failed" || !strings.Contains(detail("wf_planfail"), "boom-plan") {
		t.Errorf("wf_planfail = %+v, want failed carrying the plan error", run)
	}
	if run := runs["wf_applyfail"]; run.status != "failed" || !strings.Contains(detail("wf_applyfail"), "boom-apply") {
		t.Errorf("wf_applyfail = %+v, want failed carrying the apply error", run)
	}
	if run := runs["wf_staged"]; run.status != "requires_approval" || !strings.Contains(detail("wf_staged"), stagedApproval.String()) {
		t.Errorf("wf_staged = %+v, want requires_approval carrying the staging pointer", run)
	}
}

// assertRejectionBlocksParkedRun proves the approval loop's terminal
// outcome: an APPROVED decision keeps the run parked (the effect lands
// through redemption, not this consumer); a REJECTED one records
// 'blocked' with which approval and why.
func assertRejectionBlocksParkedRun(t *testing.T, fx *autoFixture, engine *WorkflowEngine, stagedApproval ids.ApprovalID) {
	t.Helper()
	decided := func(verdict string) kevents.Envelope {
		payload, err := json.Marshal(map[string]string{"verdict": verdict})
		if err != nil {
			t.Fatal(err)
		}
		return kevents.Envelope{
			EventID:     ids.NewV7(),
			Type:        "approval.decided",
			WorkspaceID: fx.ws,
			Entity:      kevents.EntityRef{Type: "approval", ID: stagedApproval.UUID},
			Payload:     payload,
		}
	}
	if err := engine.HandleApprovalDecided(context.Background(), decided("approved")); err != nil {
		t.Fatal(err)
	}
	if run := fx.runsByHandler(t)["wf_staged"]; run.status != "requires_approval" {
		t.Fatalf("an approved decision moved the run to %q — only redemption may complete it", run.status)
	}
	if err := engine.HandleApprovalDecided(context.Background(), decided("rejected")); err != nil {
		t.Fatal(err)
	}
	run := fx.runsByHandler(t)["wf_staged"]
	if run.status != "blocked" {
		t.Fatalf("rejected staging left the run %q, want blocked", run.status)
	}
	if run.detail == nil || !strings.Contains(*run.detail, stagedApproval.String()) || !strings.Contains(*run.detail, "rejected") {
		t.Fatalf("blocked reason %v does not name the approval and the rejection", run.detail)
	}
}
