// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package main

// The Surface-A1 end-to-end: a real workspace, a real passport, the real
// stdio server over pipes — initialize, tools/list, then the governed
// calls: 🟢 create/search/read/log/advance(open→open), the 🟡 floor on
// advance→won (zero side effects), scope refusal on a read-only passport,
// and the agent:* provenance the store must stamp. The fixture and stdio
// client live in mcpenv_integration_test.go.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func TestMCPSurfaceEndToEnd(t *testing.T) {
	e := setupMCPEnv(t)
	c := startMCP(t, e.rw.Token, "agent-test", e.svc, e.pool)

	assertHandshakeAndSurface(t, c)
	personID := exerciseGreenTools(t, e, c)
	dealID, winArgs, staged := stageWonAdvance(t, e, c)
	exerciseApprovalLoop(t, e, c, dealID, winArgs, extractApprovalID(t, staged))
	exerciseStaleAndRejectedApprovals(t, e, c, personID)
	exerciseMergeLoop(t, e, c)
	exerciseReadOnlyAndRevocation(t, e)
}

// assertHandshakeAndSurface checks the protocol handshake and that the
// declared tool surface carries every governed verb.
func assertHandshakeAndSurface(t *testing.T, c *mcpClient) {
	t.Helper()
	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(c.rpc("initialize", map[string]any{}), &init); err != nil || init.ProtocolVersion == "" {
		t.Fatalf("initialize: %v (%+v)", err, init)
	}
	var list struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(c.rpc("tools/list", map[string]any{}), &list); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range list.Tools {
		names[tool.Name] = true
	}
	for _, want := range []string{"search_records", "read_record", "create_record", "update_record", "log_activity", "advance_deal"} {
		if !names[want] {
			t.Fatalf("tools/list is missing %s (surface: %v)", want, list.Tools)
		}
	}
}

// exerciseGreenTools runs the 🟢 lane: create with server-stamped agent
// provenance, then the search/read/log round-trip. Returns the created
// person's id.
func exerciseGreenTools(t *testing.T, e *mcpEnv, c *mcpClient) ids.UUID {
	t.Helper()
	text, isErr := c.callTool("create_record", map[string]any{
		"record_type": "person",
		"fields":      map[string]any{"full_name": "Agent Made", "title": "CTO"},
	})
	if isErr {
		t.Fatalf("create_record: %s", text)
	}
	var created struct {
		ID     ids.UUID `json:"id"`
		Fields struct {
			CapturedBy string `json:"captured_by"`
			Source     string `json:"source"`
		} `json:"fields"`
	}
	if err := json.Unmarshal([]byte(text), &created); err != nil {
		t.Fatal(err)
	}
	if want := "agent:" + e.rw.ID.String(); created.Fields.CapturedBy != want {
		t.Errorf("captured_by = %q, want the server-stamped %q", created.Fields.CapturedBy, want)
	}
	if created.Fields.Source != "mcp" {
		t.Errorf("source = %q, want mcp", created.Fields.Source)
	}

	text, isErr = c.callTool("search_records", map[string]any{"q": "Agent Made", "record_type": "person"})
	if isErr || !strings.Contains(text, created.ID.String()) {
		t.Fatalf("search_records did not find the created person: err=%v %s", isErr, text)
	}
	if text, isErr = c.callTool("read_record", map[string]any{"record_type": "person", "id": created.ID}); isErr {
		t.Fatalf("read_record: %s", text)
	}

	if text, isErr = c.callTool("log_activity", map[string]any{
		"kind": "note", "subject": "Agent's note",
		"links": []map[string]any{{"entity_type": "person", "entity_id": created.ID}},
	}); isErr {
		t.Fatalf("log_activity: %s", text)
	}
	return created.ID
}

// stageWonAdvance drives advance_deal through its tier split: open→open
// is 🟢 and executes, →won is 🟡 and must stage an approval with ZERO side
// effects. Returns the deal id, the winning args, and the staging text
// carrying the approval reference.
func stageWonAdvance(t *testing.T, e *mcpEnv, c *mcpClient) (ids.UUID, map[string]any, string) {
	t.Helper()
	pipelineID, openA, openB, wonStage := seededStages(t, e.owner, e.admin.WorkspaceID.UUID)
	text, isErr := c.callTool("create_record", map[string]any{
		"record_type": "deal",
		"fields": map[string]any{
			"name": "Agent deal", "pipeline_id": pipelineID, "stage_id": openA,
		},
	})
	if isErr {
		t.Fatalf("create deal: %s", text)
	}
	var deal struct {
		ID ids.UUID `json:"id"`
	}
	if err := json.Unmarshal([]byte(text), &deal); err != nil {
		t.Fatal(err)
	}

	if text, isErr = c.callTool("advance_deal", map[string]any{"deal_id": deal.ID, "to_stage_id": openB}); isErr {
		t.Fatalf("open→open advance should be 🟢: %s", text)
	}
	winArgs := map[string]any{"deal_id": deal.ID, "to_stage_id": wonStage}
	text, isErr = c.callTool("advance_deal", winArgs)
	if !isErr || !strings.Contains(text, "staged as approval") {
		t.Fatalf("advance→won must stage an approval, got err=%v %s", isErr, text)
	}
	var stageAfter, statusAfter string
	if err := e.owner.QueryRow(context.Background(), `SELECT stage_id::text, status FROM deal WHERE id = $1`, deal.ID).
		Scan(&stageAfter, &statusAfter); err != nil {
		t.Fatal(err)
	}
	if stageAfter != openB || statusAfter != "open" {
		t.Fatalf("the refused 🟡 call left side effects: stage=%s status=%s", stageAfter, statusAfter)
	}
	return deal.ID, winArgs, text
}

// exerciseApprovalLoop runs the full 🟡 loop on the staged win: inbox
// visibility (including the C3 low-privilege blindness), the
// no-decision/no-self-approval refusals, the human decision, tamper
// rejection, the single redemption, and single-use exhaustion.
func exerciseApprovalLoop(t *testing.T, e *mcpEnv, c *mcpClient, dealID ids.UUID, winArgs map[string]any, approvalID ids.UUID) {
	t.Helper()
	// The staged item sits in the human inbox.
	pending, err := e.approvalsSvc.List(e.humanCtx, strPtr("pending"), 50)
	if err != nil || len(pending) != 1 {
		t.Fatalf("inbox: %v (%d items)", err, len(pending))
	}

	// C3: a human who could not DECIDE the staged action cannot even SEE it.
	// A low-privilege viewer (no grants) gets an empty inbox and a
	// not-found on the id — the inbox is never a workspace-wide side channel
	// leaking proposed_change/diffs.
	strangerCtx := principal.WithCorrelationID(principal.WithActor(e.wsCtx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:stranger", UserID: ids.NewV7(),
	}), ids.NewV7())
	if leaked, err := e.approvalsSvc.List(strangerCtx, strPtr("pending"), 50); err != nil || len(leaked) != 0 {
		t.Fatalf("C3: low-priv inbox leaked %d items (err=%v), want 0", len(leaked), err)
	}
	if _, err := e.approvalsSvc.Get(strangerCtx, approvalID); !errors.Is(err, apperrors.ErrNotFound) {
		t.Fatalf("C3: low-priv Get on a foreign approval → %v, want ErrNotFound", err)
	}

	// Redemption before approval is refused; an agent cannot decide.
	text, isErr := c.callTool("advance_deal", withApproval(winArgs, approvalID))
	if !isErr || !strings.Contains(text, "pending") {
		t.Fatalf("undedecided approval redeemed: err=%v %s", isErr, text)
	}
	agentCtx := principal.WithCorrelationID(principal.WithActor(e.wsCtx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:" + e.rw.ID.String(), PassportID: e.rw.ID.UUID,
	}), ids.NewV7())
	if _, err := e.approvalsSvc.Decide(agentCtx, approvalID, true, nil); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("an agent decided its own staging: %v", err)
	}

	// The human approves; the agent repeats the IDENTICAL call + approval_id.
	if _, err := e.approvalsSvc.Decide(e.humanCtx, approvalID, true, nil); err != nil {
		t.Fatalf("approve: %v", err)
	}
	// A retry with DIFFERENT args must not ride the approval.
	tampered := withApproval(map[string]any{"deal_id": dealID, "to_stage_id": winArgs["to_stage_id"], "lost_reason": "sneaky"}, approvalID)
	if text, isErr = c.callTool("advance_deal", tampered); !isErr || !strings.Contains(text, "differs") {
		t.Fatalf("tampered redemption passed: err=%v %s", isErr, text)
	}
	if text, isErr = c.callTool("advance_deal", withApproval(winArgs, approvalID)); isErr {
		t.Fatalf("approved redemption failed: %s", text)
	}
	var statusAfter string
	if err := e.owner.QueryRow(context.Background(), `SELECT status FROM deal WHERE id = $1`, dealID).Scan(&statusAfter); err != nil {
		t.Fatal(err)
	}
	if statusAfter != "won" {
		t.Fatalf("deal status = %s after redeemed close, want won", statusAfter)
	}
	// Single-use: the same approval cannot authorize a second effect.
	if text, isErr = c.callTool("advance_deal", withApproval(winArgs, approvalID)); !isErr || !strings.Contains(text, "redeemed") {
		t.Fatalf("approval redeemed twice: err=%v %s", isErr, text)
	}
}

// exerciseStaleAndRejectedApprovals pins two never-execute paths: an
// approval whose target row changed after staging (version skew), and a
// rejected staging that must never become authority.
func exerciseStaleAndRejectedApprovals(t *testing.T, e *mcpEnv, c *mcpClient, personID ids.UUID) {
	t.Helper()
	archiveArgs := map[string]any{"record_type": "person", "id": personID}
	text, _ := c.callTool("archive_record", archiveArgs)
	skewID := extractApprovalID(t, text)
	var isErr bool
	if text, isErr = c.callTool("update_record", map[string]any{
		"record_type": "person", "id": personID, "fields": map[string]any{"title": "CEO"},
	}); isErr {
		t.Fatalf("bump: %s", text)
	}
	if _, err := e.approvalsSvc.Decide(e.humanCtx, skewID, true, nil); err != nil {
		t.Fatal(err)
	}
	if text, isErr = c.callTool("archive_record", withApproval(archiveArgs, skewID)); !isErr || !strings.Contains(text, "changed since") {
		t.Fatalf("stale approval executed against a changed row: err=%v %s", isErr, text)
	}

	// Reject path: a rejected staging never becomes authority.
	text, _ = c.callTool("archive_record", archiveArgs)
	rejectID := extractApprovalID(t, text)
	if _, err := e.approvalsSvc.Decide(e.humanCtx, rejectID, false, strPtr("keep them")); err != nil {
		t.Fatal(err)
	}
	if text, isErr = c.callTool("archive_record", withApproval(archiveArgs, rejectID)); !isErr || !strings.Contains(text, "rejected") {
		t.Fatalf("rejected approval redeemed: err=%v %s", isErr, text)
	}
}

// exerciseMergeLoop: merge_records rides the same 🟡 loop — two people
// staged → human approves → the agent redeems the identical call and the
// survivor absorbs the source.
func exerciseMergeLoop(t *testing.T, e *mcpEnv, c *mcpClient) {
	t.Helper()
	mkPerson := func(name string) ids.UUID {
		text, isErr := c.callTool("create_record", map[string]any{
			"record_type": "person", "fields": map[string]any{"full_name": name},
		})
		if isErr {
			t.Fatalf("create %s: %s", name, text)
		}
		var made struct {
			ID ids.UUID `json:"id"`
		}
		if err := json.Unmarshal([]byte(text), &made); err != nil {
			t.Fatal(err)
		}
		return made.ID
	}
	mergeSrc, mergeTgt := mkPerson("Dupe A"), mkPerson("Dupe B")
	mergeArgs := map[string]any{"record_type": "person", "source_id": mergeSrc, "target_id": mergeTgt}
	text, isErr := c.callTool("merge_records", mergeArgs)
	if !isErr || !strings.Contains(text, "staged as approval") {
		t.Fatalf("merge_records must stage a 🟡 approval, got err=%v %s", isErr, text)
	}
	mergeID := extractApprovalID(t, text)
	if _, err := e.approvalsSvc.Decide(e.humanCtx, mergeID, true, nil); err != nil {
		t.Fatalf("approve merge: %v", err)
	}
	if text, isErr = c.callTool("merge_records", withApproval(mergeArgs, mergeID)); isErr {
		t.Fatalf("approved merge did not redeem: %s", text)
	}
	if !strings.Contains(text, mergeTgt.String()) {
		t.Errorf("merge result names survivor? %s", text)
	}
	// The source is now merged away — reading it as an agent 404s.
	if text, isErr = c.callTool("read_record", map[string]any{"record_type": "person", "id": mergeSrc}); !isErr {
		t.Errorf("merged-away source still reads live: %s", text)
	}
}

// exerciseReadOnlyAndRevocation: a read-only passport cannot reach a
// write tool — refused at the gate, before the handler — and revocation
// binds on the NEXT call of a live session, no reconnect needed.
func exerciseReadOnlyAndRevocation(t *testing.T, e *mcpEnv) {
	t.Helper()
	roClient := startMCP(t, e.ro.Token, "agent-test", e.svc, e.pool)
	text, isErr := roClient.callTool("create_record", map[string]any{
		"record_type": "person", "fields": map[string]any{"full_name": "Should not exist"},
	})
	if !isErr || !strings.Contains(text, "scope") {
		t.Fatalf("read-only passport wrote: err=%v %s", isErr, text)
	}
	if text, isErr = roClient.callTool("search_records", map[string]any{"q": "Agent Made"}); isErr {
		t.Fatalf("read-only passport should read: %s", text)
	}

	if err := e.svc.RevokePassport(e.wsCtx, e.admin, e.ro.ID); err != nil {
		t.Fatal(err)
	}
	if text, isErr = roClient.callTool("search_records", map[string]any{"q": "x"}); !isErr || !strings.Contains(text, "authentication") {
		t.Fatalf("revoked passport kept working: err=%v %s", isErr, text)
	}
}
