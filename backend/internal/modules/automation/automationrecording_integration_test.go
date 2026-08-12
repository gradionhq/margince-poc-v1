// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package automation

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
			return workflow.RunResult{}, &workflow.StagedApprovalError{ApprovalID: stagedApproval}
		}},
	}
	engine := NewWorkflowEngine(fx.pool, nil) // nil resolver: these fixtures carry no owner_id, so the match-time gate skips before ever touching it
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

// TestFailedRunReasonNeverLeaksRawProviderErrorInternals proves the T2 half
// of B-E15.3a's honest recording: a provider's raw error — exactly what a
// wrapped pgx failure carries (a SQLSTATE code, a table or column name) —
// must never reach workflow_run.detail, because that column is read
// verbatim by any workspace user holding automation:read (GET
// /automations/{id}/runs, handlers_automations.go). Against the pre-fix
// code (reasonDetail(applyErr.Error())) this test fails on the
// "secret_table" substring; sanitizedReason (engine_run.go) is what
// makes it pass.
func TestFailedRunReasonNeverLeaksRawProviderErrorInternals(t *testing.T) {
	fx := setupAutomationDB(t)
	rawProviderErr := errors.New(`pq: relation "secret_table" does not exist (SQLSTATE 42P01)`)
	h := scriptedWorkflow{
		name: "wf_leaky_provider",
		apply: func(workflow.Event) (workflow.RunResult, error) {
			return workflow.RunResult{}, rawProviderErr
		},
	}
	fx.seedAutomation(t, h.name)
	engine := NewWorkflowEngine(fx.pool, nil) // nil resolver: this fixture carries no owner_id, so the match-time gate skips before ever touching it
	engine.RegisterWorkflow(h)

	env := kevents.Envelope{
		EventID:     ids.NewV7(),
		Type:        scriptedTrigger,
		WorkspaceID: fx.ws,
		OccurredAt:  time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC),
		Entity:      kevents.EntityRef{Type: "deal", ID: ids.NewV7()},
	}
	if err := engine.HandleEvent(context.Background(), env); err == nil {
		t.Fatal("HandleEvent swallowed the apply failure")
	}

	run := fx.runsByHandler(t)[h.name]
	if run.status != "failed" {
		t.Fatalf("status = %q, want failed", run.status)
	}
	reason, err := decodeRunDetail(run.detail)
	if err != nil {
		t.Fatalf("detail did not decode: %v", err)
	}
	if reason == nil || *reason == "" {
		t.Fatal("a failed run must still carry a human-readable reason")
	}
	if strings.Contains(*reason, "secret_table") || strings.Contains(*reason, "SQLSTATE") || strings.Contains(*reason, "pq:") {
		t.Fatalf("detail.reason = %q, leaks the raw provider error's internals", *reason)
	}
}

// assertRecordedOutcomes checks each scripted handler landed exactly one
// run row in its terminal state, reason decoded from the detail column
// through the SAME reader the run-history API uses.
func assertRecordedOutcomes(t *testing.T, fx *autoFixture, wantRuns int, stagedApproval ids.ApprovalID) {
	t.Helper()
	runs := fx.runsByHandler(t)
	if len(runs) != wantRuns {
		t.Fatalf("recorded %d runs, want one per handler (%d) — an outcome went unrecorded", len(runs), wantRuns)
	}
	reasonOf := func(name string) string {
		reason, err := decodeRunDetail(runs[name].detail)
		if err != nil {
			t.Fatalf("%s: detail did not decode: %v", name, err)
		}
		if reason == nil {
			t.Fatalf("%s: run has no reason in its detail", name)
		}
		return *reason
	}
	if run := runs["wf_ok"]; run.status != "applied" || run.detail != nil {
		t.Errorf("wf_ok = %+v, want a clean applied run", run)
	}
	if run := runs["wf_skip"]; run.status != "skipped" || run.planned != "[]" {
		t.Errorf("wf_skip = %+v, want skipped with an empty plan", run)
	} else if !strings.Contains(reasonOf("wf_skip"), "did not satisfy") {
		t.Errorf("wf_skip reason %q does not say why nothing happened", reasonOf("wf_skip"))
	}
	// The reason names WHICH phase failed (a human reading run history needs
	// that), but never repeats the raw handler/provider error text: a real
	// Match/Plan/Apply failure can carry a raw pgx error (SQLSTATE, table,
	// column names), and this column is read verbatim by any workspace user
	// with automation:read (T2, engine_run.go's sanitizedReason).
	if run := runs["wf_matchfail"]; run.status != "failed" ||
		!strings.Contains(reasonOf("wf_matchfail"), "matching the trigger") ||
		strings.Contains(reasonOf("wf_matchfail"), "boom-match") {
		t.Errorf("wf_matchfail = %+v, want failed naming the match phase without the raw match error", run)
	}
	if run := runs["wf_planfail"]; run.status != "failed" ||
		!strings.Contains(reasonOf("wf_planfail"), "planning the effect") ||
		strings.Contains(reasonOf("wf_planfail"), "boom-plan") {
		t.Errorf("wf_planfail = %+v, want failed naming the plan phase without the raw plan error", run)
	}
	if run := runs["wf_applyfail"]; run.status != "failed" ||
		reasonOf("wf_applyfail") == "" || strings.Contains(reasonOf("wf_applyfail"), "boom-apply") {
		t.Errorf("wf_applyfail = %+v, want failed with a sanitized, non-empty reason that omits the raw apply error", run)
	}
	run := runs["wf_staged"]
	if run.status != "requires_approval" {
		t.Fatalf("wf_staged = %+v, want requires_approval", run)
	}
	staged, err := parseRunDetail(run.detail)
	if err != nil {
		t.Fatalf("wf_staged: detail did not decode: %v", err)
	}
	// The staging pointer is a parsed jsonb field, not a substring of the
	// reason sentence — proving MarkRunBlocked can match on it structurally.
	if staged.ApprovalID == nil || *staged.ApprovalID != stagedApproval {
		t.Errorf("wf_staged detail = %+v, want approval_id = %s", staged, stagedApproval)
	}
	if !strings.Contains(staged.Reason, stagedApproval.String()) {
		t.Errorf("wf_staged reason %q does not name the approval", staged.Reason)
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
	reason, err := decodeRunDetail(run.detail)
	if err != nil {
		t.Fatalf("blocked run detail did not decode: %v", err)
	}
	if reason == nil || !strings.Contains(*reason, stagedApproval.String()) || !strings.Contains(*reason, "rejected") {
		t.Fatalf("blocked reason %v does not name the approval and the rejection", reason)
	}
}
