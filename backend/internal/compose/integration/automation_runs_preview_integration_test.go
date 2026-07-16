// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The automation observability surface over HTTP (A72/ADR-0035 Am.1):
// /runs renders the engine's honest history — every outcome, each with
// its reason/approval/target trace — and /preview answers the designer's
// dry-run blast radius without writing a single domain, audit, or outbox
// row. Both hide absent instances exactly like GET /automations/{id}.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// automationRunWire is the contract AutomationRun shape as this suite
// reads it; pointers make absent-vs-present assertable.
type automationRunWire struct {
	ID               string  `json:"id"`
	AutomationID     string  `json:"automation_id"`
	Outcome          string  `json:"outcome"`
	Tier             string  `json:"tier"`
	OccurredAt       string  `json:"occurred_at"`
	Reason           *string `json:"reason"`
	ApprovalRequired *bool   `json:"approval_required"`
	TriggerEvidence  *string `json:"trigger_evidence"`
	TargetRef        *string `json:"target_ref"`
	ActionResult     *string `json:"action_result"`
}

type automationRunsPage struct {
	Data []automationRunWire `json:"data"`
	Page struct {
		HasMore bool `json:"has_more"`
	} `json:"page"`
}

// createRouteLeadAutomation creates one valid route_lead instance over
// the API and returns its id (it lands paused; runs/preview do not care).
func createRouteLeadAutomation(t *testing.T, e *env) string {
	t.Helper()
	var created struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	if status := e.call(t, "POST", "/v1/automations", anyMap{
		"key": "route_lead", "name": "Router under observation",
		"params": anyMap{"owners": []string{"0198c0de-0000-7000-8000-000000000001"}, "cap_per_owner": 3},
	}, nil, &created); status != http.StatusCreated {
		t.Fatalf("create automation → %d", status)
	}
	return created.ID
}

// workspaceIDBySlug resolves the bootstrapped tenant's id through the
// owner connection for RLS-free seeding.
func workspaceIDBySlug(t *testing.T, e *env) string {
	t.Helper()
	var wsID string
	if err := e.owner.QueryRow(context.Background(),
		`SELECT id FROM workspace WHERE slug = $1`, e.slug).Scan(&wsID); err != nil {
		t.Fatalf("workspace lookup: %v", err)
	}
	return wsID
}

// seedWorkflowRun records one engine firing linked the way the engine
// links them: handler = catalog key, idempotency key suffixed with
// "@<automation id>". planned/applied ride the workflow.Action encoding;
// detail is the raw jsonb payload in the automation module's rundetail.go
// shape (nil for a clean run) — this suite seeds rows directly at the DB,
// bypassing the engine, so it must match that shape for the HTTP read
// side (ListRuns/wireAutomationRun) to render the reason correctly.
func seedWorkflowRun(t *testing.T, e *env, wsID, automationID, status string, planned, applied *string, detail []byte, at time.Time) string {
	t.Helper()
	runID := ids.NewV7().String()
	plannedJSON := "[]"
	if planned != nil {
		plannedJSON = *planned
	}
	if _, err := e.owner.Exec(context.Background(), `
		INSERT INTO workflow_run (id, workspace_id, handler, idempotency_key, trigger_event, planned, applied, status, detail, created_at)
		VALUES ($1, $2, 'route_lead', $3, $4, $5, $6, $7, $8, $9)`,
		runID, wsID, fmt.Sprintf("route_lead:%s@%s", runID, automationID),
		ids.NewV7(), plannedJSON, applied, status, detail, at); err != nil {
		t.Fatalf("seeding workflow_run: %v", err)
	}
	return runID
}

// runDetailReason builds the workflow_run.detail jsonb payload for a
// plain reason, matching the shape the automation module's writers
// produce (rundetail.go's runDetail{Reason: ...}, unexported there — this
// suite seeds the row directly at the DB, so it reproduces the wire
// shape rather than importing the type).
func runDetailReason(t *testing.T, reason string) []byte {
	t.Helper()
	payload, err := json.Marshal(struct {
		Reason string `json:"reason"`
	}{Reason: reason})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

// requireStr asserts a wire pointer field is present with the wanted value.
func requireStr(t *testing.T, field string, got *string, want string) {
	t.Helper()
	if got == nil || *got != want {
		t.Fatalf("%s = %v, want %q", field, got, want)
	}
}

func TestAutomationRunHistoryAndPreviewOverHTTP(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)
	autoID := createRouteLeadAutomation(t, e)

	assertRunsStartEmptyAndHideAbsentInstances(t, e, autoID)
	assertRunsRenderEveryOutcomeWithItsTrace(t, e, autoID)
	assertPreviewMeasuresWithoutWriting(t, e, autoID)
}

// assertRunsStartEmptyAndHideAbsentInstances pins the empty-history page
// shape, existence-hiding on an unknown id, and the 422 on an outcome
// outside the wire vocabulary.
func assertRunsStartEmptyAndHideAbsentInstances(t *testing.T, e *env, autoID string) {
	t.Helper()
	var page automationRunsPage
	if status := e.call(t, "GET", "/v1/automations/"+autoID+"/runs", nil, nil, &page); status != http.StatusOK {
		t.Fatalf("runs on a never-fired automation → %d", status)
	}
	if page.Data == nil || len(page.Data) != 0 || page.Page.HasMore {
		t.Fatalf("never-fired history = %+v, want the honest empty page (data [], has_more false)", page)
	}

	unknown := ids.NewV7().String()
	if status := e.call(t, "GET", "/v1/automations/"+unknown+"/runs", nil, nil, nil); status != http.StatusNotFound {
		t.Fatalf("runs on an unknown automation → %d, want 404", status)
	}
	if status := e.call(t, "GET", "/v1/automations/"+autoID+"/runs?outcome=exploded", nil, nil, nil); status != 422 {
		t.Fatalf("outcome outside the vocabulary → %d, want 422", status)
	}
}

// assertRunsRenderEveryOutcomeWithItsTrace seeds one run per interesting
// engine status and checks the wire mapping: reason rides only reasoned
// outcomes, approval_required marks the parked/blocked ones, and the
// fired run names its action kinds and first target.
func assertRunsRenderEveryOutcomeWithItsTrace(t *testing.T, e *env, autoID string) {
	t.Helper()
	wsID := workspaceIDBySlug(t, e)
	targetLead := ids.NewV7().String()
	actionTrace := fmt.Sprintf(`[{"Kind":"assign_owner","Target":{"Type":"lead","ID":"%s"},"Args":{}}]`, targetLead)
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	reason := func(s string) []byte { return runDetailReason(t, s) }

	seedWorkflowRun(t, e, wsID, autoID, "applied", &actionTrace, &actionTrace, nil, base)
	seedWorkflowRun(t, e, wsID, autoID, "failed", nil, nil, reason("provider error"), base.Add(time.Minute))
	seedWorkflowRun(t, e, wsID, autoID, "requires_approval", &actionTrace, nil,
		reason("staged as approval "+ids.NewV7().String()+"; awaiting the human decision"), base.Add(2*time.Minute))

	var page automationRunsPage
	if status := e.call(t, "GET", "/v1/automations/"+autoID+"/runs", nil, nil, &page); status != http.StatusOK {
		t.Fatalf("runs → %d", status)
	}
	if len(page.Data) != 3 {
		t.Fatalf("history holds %d runs, want the 3 seeded", len(page.Data))
	}
	// Newest first: parked, failed, fired.
	parked, failed, fired := page.Data[0], page.Data[1], page.Data[2]
	if parked.Outcome != "queued_for_approval" || failed.Outcome != "failed" || fired.Outcome != "fired" {
		t.Fatalf("outcomes newest-first = %s/%s/%s, want queued_for_approval/failed/fired",
			parked.Outcome, failed.Outcome, fired.Outcome)
	}
	for _, run := range page.Data {
		if run.AutomationID != autoID || run.Tier != "green" {
			t.Fatalf("run %s carries automation %s tier %s, want the parent %s at tier green", run.ID, run.AutomationID, run.Tier, autoID)
		}
		if run.TriggerEvidence == nil || !strings.HasPrefix(*run.TriggerEvidence, "triggered by event ") {
			t.Fatalf("run %s trigger_evidence = %v, want the triggering-event line", run.ID, run.TriggerEvidence)
		}
	}

	requireStr(t, "fired action_result", fired.ActionResult, "applied assign_owner")
	requireStr(t, "fired target_ref", fired.TargetRef, "lead:"+targetLead)
	if fired.Reason != nil || fired.ApprovalRequired != nil {
		t.Fatalf("a fired run carries reason=%v approval_required=%v, want neither", fired.Reason, fired.ApprovalRequired)
	}

	requireStr(t, "failed reason", failed.Reason, "provider error")
	if failed.ActionResult != nil || failed.TargetRef != nil {
		t.Fatalf("a pre-plan failure carries action_result=%v target_ref=%v, want neither (its plan is honestly empty)",
			failed.ActionResult, failed.TargetRef)
	}

	requireStr(t, "parked action_result", parked.ActionResult, "queued to approval inbox")
	if parked.ApprovalRequired == nil || !*parked.ApprovalRequired {
		t.Fatalf("a parked run reads approval_required=%v, want true", parked.ApprovalRequired)
	}

	// The wire outcome filter narrows to exactly the fired run.
	var onlyFired automationRunsPage
	if status := e.call(t, "GET", "/v1/automations/"+autoID+"/runs?outcome=fired", nil, nil, &onlyFired); status != http.StatusOK {
		t.Fatalf("outcome=fired → %d", status)
	}
	if len(onlyFired.Data) != 1 || onlyFired.Data[0].ID != fired.ID {
		t.Fatalf("outcome=fired returned %d runs, want exactly the applied one", len(onlyFired.Data))
	}
}

// assertPreviewMeasuresWithoutWriting pins the dry-run: the stored
// route_lead recipe counts the open unrouted lead pool, the draft
// override previews another catalog type before saving, the editor's 422
// vocabulary matches a save's, and the whole thing leaves zero rows.
func assertPreviewMeasuresWithoutWriting(t *testing.T, e *env, autoID string) {
	t.Helper()
	var lead struct {
		ID string `json:"id"`
	}
	if status := e.call(t, "POST", "/v1/leads", anyMap{
		"full_name": "Uma Unrouted", "source": "manual",
	}, nil, &lead); status != http.StatusCreated {
		t.Fatalf("create lead → %d", status)
	}

	var rowsBefore int
	if err := e.owner.QueryRow(context.Background(), `
		SELECT (SELECT count(*) FROM audit_log)
		     + (SELECT count(*) FROM event_outbox)
		     + (SELECT count(*) FROM workflow_run)`).Scan(&rowsBefore); err != nil {
		t.Fatal(err)
	}

	var preview struct {
		MatchesNow           int       `json:"matches_now"`
		WindowDays           int       `json:"window_days"`
		WouldHaveFired       *int      `json:"would_have_fired"`
		Sample               *[]string `json:"sample"`
		ExcludedByPermission *int      `json:"excluded_by_permission"`
	}
	if status := e.call(t, "POST", "/v1/automations/"+autoID+"/preview", anyMap{}, nil, &preview); status != http.StatusOK {
		t.Fatalf("preview → %d", status)
	}
	if preview.MatchesNow != 1 || preview.WindowDays != 30 {
		t.Fatalf("preview = %d matches over %d days, want the 1 unrouted lead over the default 30", preview.MatchesNow, preview.WindowDays)
	}
	if preview.WouldHaveFired == nil || *preview.WouldHaveFired != 1 {
		t.Fatalf("would_have_fired = %v, want 1 (the lead created inside the window)", preview.WouldHaveFired)
	}
	if preview.Sample == nil || len(*preview.Sample) != 1 || (*preview.Sample)[0] != lead.ID {
		t.Fatalf("sample = %v, want exactly the matching lead %s", preview.Sample, lead.ID)
	}
	// Zero ships explicitly: "nothing was hidden" is information.
	if preview.ExcludedByPermission == nil || *preview.ExcludedByPermission != 0 {
		t.Fatalf("excluded_by_permission = %v, want an explicit 0", preview.ExcludedByPermission)
	}

	// The draft override previews an edited recipe before it is saved.
	var draft struct {
		MatchesNow int `json:"matches_now"`
		WindowDays int `json:"window_days"`
	}
	if status := e.call(t, "POST", "/v1/automations/"+autoID+"/preview", anyMap{
		"key": "stage_change_create_task", "window_days": 7,
	}, nil, &draft); status != http.StatusOK {
		t.Fatalf("draft-override preview → %d", status)
	}
	if draft.MatchesNow != 0 || draft.WindowDays != 7 {
		t.Fatalf("draft preview = %d matches over %d days, want 0 open deals over the requested 7", draft.MatchesNow, draft.WindowDays)
	}

	// The editor's preview 422s match its save 422s.
	if status := e.call(t, "POST", "/v1/automations/"+autoID+"/preview", anyMap{"key": "invented_type"}, nil, nil); status != 422 {
		t.Fatalf("preview with a non-catalog key → %d, want 422", status)
	}
	if status := e.call(t, "POST", "/v1/automations/"+autoID+"/preview", anyMap{"window_days": 0}, nil, nil); status != 422 {
		t.Fatalf("preview with a zero window → %d, want 422", status)
	}
	unknown := ids.NewV7().String()
	if status := e.call(t, "POST", "/v1/automations/"+unknown+"/preview", anyMap{}, nil, nil); status != http.StatusNotFound {
		t.Fatalf("preview on an unknown automation → %d, want 404 (existence-hiding, like Get)", status)
	}

	var rowsAfter int
	if err := e.owner.QueryRow(context.Background(), `
		SELECT (SELECT count(*) FROM audit_log)
		     + (SELECT count(*) FROM event_outbox)
		     + (SELECT count(*) FROM workflow_run)`).Scan(&rowsAfter); err != nil {
		t.Fatal(err)
	}
	if rowsAfter != rowsBefore {
		t.Fatalf("previewing wrote %d rows across audit/outbox/run — a preview is a read", rowsAfter-rowsBefore)
	}
}
