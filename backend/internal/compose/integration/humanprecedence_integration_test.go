// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The human-edit-precedence gate (interfaces.md §2.1) is per
// FIELD, on both transports: an agent's patch applies 🟢 wherever it
// keeps clear of human-typed values, the human-owned fields it touches
// are split off into a 🟡 staged approval in the same request, and only
// a human decision — consumed exactly once — releases the overwrite.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func TestEndToEnd_humanEditPrecedenceOnAgentUpdate(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)

	// A HUMAN types the person's name — full_name is now human-owned per
	// the audit trail.
	var person struct {
		ID string `json:"id"`
	}
	if status := e.call(t, "POST", "/v1/people", anyMap{"full_name": "Greta Human"}, nil, &person); status != 201 {
		t.Fatalf("human create → %d", status)
	}

	var minted struct {
		Token string `json:"token"`
	}
	if status := e.call(t, "POST", "/v1/passports", anyMap{
		"label": "precedence agent", "scopes": []string{"read", "write"},
	}, nil, &minted); status != 201 {
		t.Fatalf("issue passport → %d", status)
	}
	bearer := map[string]string{"Authorization": "Bearer " + minted.Token}

	// A field no human ever wrote updates 🟢 — the reversible-and-logged
	// path stays open.
	if status := e.call(t, "PATCH", "/v1/people/"+person.ID, anyMap{"title": "CTO"}, bearer, nil); status != 200 {
		t.Fatalf("agent patch of a never-human field → %d, want 200 (🟢)", status)
	}
	// A field the AGENT last wrote stays 🟢 too — precedence protects
	// people, not machines.
	if status := e.call(t, "PATCH", "/v1/people/"+person.ID, anyMap{"title": "VP Engineering"}, bearer, nil); status != 200 {
		t.Fatalf("agent re-patch of its own field → %d, want 200 (🟢)", status)
	}

	// Overwriting the human-typed name resolves 🟡: staged, not applied.
	var problem struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}
	status := e.call(t, "PATCH", "/v1/people/"+person.ID, anyMap{"full_name": "Greta Machine"}, bearer, &problem)
	if status != 403 || problem.Code != "approval_required" {
		t.Fatalf("agent overwrite of a human field → %d %q, want 403 approval_required", status, problem.Code)
	}
	var current struct {
		FullName string `json:"full_name"`
	}
	if status := e.call(t, "GET", "/v1/people/"+person.ID, nil, bearer, &current); status != 200 || current.FullName != "Greta Human" {
		t.Fatalf("staged overwrite must not have executed: %d %q", status, current.FullName)
	}
	approvalID := extractStagedApprovalID(t, problem.Detail)

	// A human decision releases it; the identical retry lands the patch.
	if status := e.call(t, "POST", "/v1/approvals/"+approvalID+"/approve", anyMap{}, nil, nil); status != 200 {
		t.Fatalf("human approve → %d", status)
	}
	withToken := map[string]string{"Authorization": "Bearer " + minted.Token, "X-Approval-Token": approvalID}
	if status := e.call(t, "PATCH", "/v1/people/"+person.ID, anyMap{"full_name": "Greta Machine"}, withToken, nil); status != 200 {
		t.Fatalf("approved retry → %d, want the patch to execute", status)
	}
	if status := e.call(t, "GET", "/v1/people/"+person.ID, nil, bearer, &current); status != 200 || current.FullName != "Greta Machine" {
		t.Fatalf("approved overwrite did not land: %d %q", status, current.FullName)
	}

	// The redemption was consumed exactly once: the identical retry with
	// the same token changes nothing further.
	before := current.FullName
	if status := e.call(t, "PATCH", "/v1/people/"+person.ID, anyMap{"full_name": "Greta Machine"}, withToken, &problem); status != 403 {
		t.Fatalf("re-redeeming a consumed approval → %d, want 403", status)
	}
	if status := e.call(t, "GET", "/v1/people/"+person.ID, nil, bearer, &current); status != 200 || current.FullName != before {
		t.Fatalf("consumed token must not write again: %d %q", status, current.FullName)
	}
}

// A differently-cased field key cannot slip a human-owned overwrite
// through the 🟢 path. The precedence probe matches audit keys
// case-sensitively (jsonb), so it clears `{"FULL_NAME":…}` as touching no
// human-owned field. The store is the backstop: every record object's
// generated request decoder matches its known fields by EXACT key, so a
// case-variant never binds to the core column via encoding/json's
// case-insensitive fallback — it lands in the additionalProperties
// catch-all and is dropped as a non-catalog custom key. The write is a
// no-op (200), and the human value is left intact. Lead is exercised here
// because it was the last object to gain the catch-all (custom fields on
// records); the protection is now uniform across person/org/deal/lead.
func TestEndToEnd_caseVariantKeyCannotBypassPrecedence(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)

	var lead struct {
		ID string `json:"id"`
	}
	if status := e.call(t, "POST", "/v1/leads", anyMap{"full_name": "Otto Human"}, nil, &lead); status != 201 {
		t.Fatalf("human create lead → %d", status)
	}
	var minted struct {
		Token string `json:"token"`
	}
	if status := e.call(t, "POST", "/v1/passports", anyMap{
		"label": "variant agent", "scopes": []string{"read", "write"},
	}, nil, &minted); status != 201 {
		t.Fatalf("issue passport → %d", status)
	}
	bearer := map[string]string{"Authorization": "Bearer " + minted.Token}

	// The case-variant key binds to no known field (exact-key decode) and no
	// active custom column, so it drops to an empty patch: 200, no write.
	var patched struct {
		FullName string `json:"full_name"`
	}
	if status := e.call(t, "PATCH", "/v1/leads/"+lead.ID, anyMap{"FULL_NAME": "Otto Machine"}, bearer, &patched); status != 200 {
		t.Fatalf("case-variant patch → %d, want 200 (dropped, not written)", status)
	}
	if patched.FullName != "Otto Human" {
		t.Fatalf("case-variant key must not overwrite the human value: got %q", patched.FullName)
	}
	// And it survives a fresh read — the human-owned value was never touched.
	var current struct {
		FullName string `json:"full_name"`
	}
	if status := e.call(t, "GET", "/v1/leads/"+lead.ID, nil, bearer, &current); status != 200 || current.FullName != "Otto Human" {
		t.Fatalf("human value must survive the case-variant attempt: %d %q", status, current.FullName)
	}
}

// stagePersonAndAgent is the scenario floor the split tests share: a
// HUMAN creates the person (full_name becomes human-owned per the audit
// trail) and mints a read+write passport for the acting agent.
func stagePersonAndAgent(t *testing.T, e *env, fullName, label string) (personID, agentToken string) {
	t.Helper()
	var person struct {
		ID string `json:"id"`
	}
	if status := e.call(t, "POST", "/v1/people", anyMap{"full_name": fullName}, nil, &person); status != 201 {
		t.Fatalf("human create → %d", status)
	}
	var minted struct {
		Token string `json:"token"`
	}
	if status := e.call(t, "POST", "/v1/passports", anyMap{
		"label": label, "scopes": []string{"read", "write"},
	}, nil, &minted); status != 201 {
		t.Fatalf("issue passport → %d", status)
	}
	return person.ID, minted.Token
}

// mcpAgentInvoker builds the governed tool registry over the shared
// database and returns an Invoke closure that re-authenticates the
// passport per call, exactly as the stdio/hosted transports do.
func mcpAgentInvoker(t *testing.T, e *env, agentToken string) func(tool, args string) (json.RawMessage, error) {
	t.Helper()
	pool, err := database.NewPool(context.Background(), envDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	registry := compose.NewRegistry(pool)
	authSvc := identity.NewService(pool)
	return func(tool, args string) (json.RawMessage, error) {
		wsID, err := authSvc.InstallationWorkspace(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		ctx := principal.WithWorkspaceID(context.Background(), wsID.UUID)
		agent, err := authSvc.AuthenticateAgent(ctx, agentToken)
		if err != nil {
			t.Fatal(err)
		}
		ctx = principal.WithCorrelationID(principal.WithActor(ctx, agent.Principal()), ids.NewV7())
		return registry.Invoke(ctx, tool, json.RawMessage(args))
	}
}

// A mixed patch splits per field (interfaces.md §2.1): the agent-safe
// fields apply at the auto-execute tier in the same PATCH, the human-owned field is staged,
// the 200 response names the split, and the approved replay of ONLY the
// staged fields — the ADR-0036-bound sub-patch — lands the overwrite.
func TestEndToEnd_perFieldSplitOnAgentRESTUpdate(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)

	personID, agentToken := stagePersonAndAgent(t, e, "Petra Human", "split agent")
	// The human TYPES the title in an update: the per-field audit image
	// makes title human-owned alongside the created full_name.
	if status := e.call(t, "PATCH", "/v1/people/"+personID, anyMap{"title": "Founder"}, nil, nil); status != 200 {
		t.Fatalf("human title patch → %d", status)
	}
	bearer := map[string]string{"Authorization": "Bearer " + agentToken}

	// Mixed patch: first_name is virgin ground, full_name and title are
	// human-typed — only they must be withheld.
	var split struct {
		FirstName      string `json:"first_name"`
		FullName       string `json:"full_name"`
		StagedApproval struct {
			ApprovalID string          `json:"approval_id"`
			Fields     []string        `json:"fields"`
			Replay     json.RawMessage `json:"replay"`
			Message    string          `json:"message"`
		} `json:"staged_approval"`
	}
	status := e.call(t, "PATCH", "/v1/people/"+personID, anyMap{
		"first_name": "Petra", "full_name": "Petra Machine", "title": "CEO",
	}, bearer, &split)
	if status != 200 {
		t.Fatalf("mixed agent patch → %d, want 200 for its auto-execute half", status)
	}
	if split.FirstName != "Petra" || split.FullName != "Petra Human" {
		t.Fatalf("auto-execute half wrong: first_name %q full_name %q — want the new first name and the untouched human name", split.FirstName, split.FullName)
	}
	if split.StagedApproval.ApprovalID == "" || len(split.StagedApproval.Fields) != 2 ||
		split.StagedApproval.Fields[0] != "full_name" || split.StagedApproval.Fields[1] != "title" {
		t.Fatalf("staged_approval = %+v, want exactly the two human-owned fields named", split.StagedApproval)
	}

	// A human approves; the agent replays ONLY the staged fields with the
	// token — the sub-patch is what the approval is bound to.
	if status := e.call(t, "POST", "/v1/approvals/"+split.StagedApproval.ApprovalID+"/approve", anyMap{}, nil, nil); status != 200 {
		t.Fatalf("human approve → %d", status)
	}
	withToken := map[string]string{"Authorization": "Bearer " + agentToken, "X-Approval-Token": split.StagedApproval.ApprovalID}

	// A replay that differs from the staged sub-patch does not fit the
	// approval (diff_hash binding) — and consumes nothing.
	var problem struct {
		Code string `json:"code"`
	}
	if status := e.call(t, "PATCH", "/v1/people/"+personID, anyMap{"full_name": "Petra Impostor", "title": "CEO"}, withToken, &problem); status != 403 || problem.Code != "approval_token_invalid" {
		t.Fatalf("tampered replay → %d %q, want 403 approval_token_invalid", status, problem.Code)
	}

	var replay anyMap
	if err := json.Unmarshal(split.StagedApproval.Replay, &replay); err != nil {
		t.Fatalf("staged replay body is not an object: %v", err)
	}
	if status := e.call(t, "PATCH", "/v1/people/"+personID, replay, withToken, nil); status != 200 {
		t.Fatalf("approved replay of the staged sub-patch → %d, want 200", status)
	}
	var current struct {
		FullName  string `json:"full_name"`
		Title     string `json:"title"`
		FirstName string `json:"first_name"`
	}
	if status := e.call(t, "GET", "/v1/people/"+personID, nil, bearer, &current); status != 200 ||
		current.FullName != "Petra Machine" || current.Title != "CEO" || current.FirstName != "Petra" {
		t.Fatalf("post-approval record = %+v, want the released overwrite plus the earlier auto-execute write", current)
	}
}

// A rejection writes nothing: the human-owned field stays as typed and
// the staged approval opens no door afterwards.
func TestEndToEnd_rejectedSplitWritesNothing(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)

	personID, agentToken := stagePersonAndAgent(t, e, "Rita Human", "rejected agent")
	bearer := map[string]string{"Authorization": "Bearer " + agentToken}

	var split struct {
		StagedApproval struct {
			ApprovalID string `json:"approval_id"`
		} `json:"staged_approval"`
	}
	if status := e.call(t, "PATCH", "/v1/people/"+personID, anyMap{
		"first_name": "Rita", "full_name": "Rita Machine",
	}, bearer, &split); status != 200 || split.StagedApproval.ApprovalID == "" {
		t.Fatalf("mixed patch → %d %+v, want 200 with a staged approval", status, split)
	}
	if status := e.call(t, "POST", "/v1/approvals/"+split.StagedApproval.ApprovalID+"/reject", anyMap{"reason": "keep my spelling"}, nil, nil); status != 200 {
		t.Fatalf("human reject → %d", status)
	}

	withToken := map[string]string{"Authorization": "Bearer " + agentToken, "X-Approval-Token": split.StagedApproval.ApprovalID}
	var problem struct {
		Code string `json:"code"`
	}
	if status := e.call(t, "PATCH", "/v1/people/"+personID, anyMap{"full_name": "Rita Machine"}, withToken, &problem); status != 403 || problem.Code != "approval_token_invalid" {
		t.Fatalf("replaying a rejected staging → %d %q, want 403 approval_token_invalid", status, problem.Code)
	}
	var current struct {
		FullName  string `json:"full_name"`
		FirstName string `json:"first_name"`
	}
	if status := e.call(t, "GET", "/v1/people/"+personID, nil, bearer, &current); status != 200 ||
		current.FullName != "Rita Human" || current.FirstName != "Rita" {
		t.Fatalf("post-rejection record = %+v, want the human name intact and only the auto-execute write applied", current)
	}
}

// replayWithApprovalID turns a staged replay body into the redeeming MCP
// call: the same arguments plus the approval reference.
func replayWithApprovalID(t *testing.T, replay json.RawMessage, approvalID string) string {
	t.Helper()
	var call map[string]json.RawMessage
	if err := json.Unmarshal(replay, &call); err != nil {
		t.Fatalf("staged replay body is not an object: %v", err)
	}
	call["approval_id"] = json.RawMessage(fmt.Sprintf("%q", approvalID))
	out, err := json.Marshal(call)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// personNameAndTitle reads a person back through the governed tool
// surface and returns the two fields the split scenarios assert on.
func personNameAndTitle(t *testing.T, invoke func(tool, args string) (json.RawMessage, error), personID string) (fullName, title string) {
	t.Helper()
	out, err := invoke("read_record", fmt.Sprintf(`{"record_type":"person","id":%q}`, personID))
	if err != nil {
		t.Fatal(err)
	}
	var rec struct {
		Fields struct {
			FullName string `json:"full_name"`
			Title    string `json:"title"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(out, &rec); err != nil {
		t.Fatal(err)
	}
	return rec.Fields.FullName, rec.Fields.Title
}

// The SAME split fires on the MCP transport — one spelling, two
// surfaces: an auto-execute-only tool patch runs to completion, a mixed patch
// applies its auto-execute half and stages the human-owned residue, and the
// approved replay (staged fields + approval_id) redeems exactly once.
func TestEndToEnd_perFieldSplitOnMCPUpdate(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)

	personID, agentToken := stagePersonAndAgent(t, e, "Mona Human", "mcp split agent")
	invoke := mcpAgentInvoker(t, e, agentToken)

	// Auto-execute-only: a field no human wrote updates without any staging.
	out, err := invoke("update_record", fmt.Sprintf(
		`{"record_type":"person","id":%q,"fields":{"title":"CTO"}}`, personID))
	if err != nil {
		t.Fatalf("auto-execute tool patch → %v", err)
	}
	if strings.Contains(string(out), "staged_approval") {
		t.Fatalf("auto-execute tool patch staged something: %s", out)
	}

	// Mixed: title is agent-owned now, full_name is the human's.
	out, err = invoke("update_record", fmt.Sprintf(
		`{"record_type":"person","id":%q,"fields":{"full_name":"Mona Machine","title":"VP Engineering"}}`, personID))
	if err != nil {
		t.Fatalf("mixed tool patch must succeed for its auto-execute half: %v", err)
	}
	var result struct {
		Fields struct {
			FullName string `json:"full_name"`
			Title    string `json:"title"`
		} `json:"fields"`
		StagedApproval struct {
			ApprovalID string          `json:"approval_id"`
			Fields     []string        `json:"fields"`
			Replay     json.RawMessage `json:"replay"`
		} `json:"staged_approval"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatal(err)
	}
	if result.Fields.FullName != "Mona Human" || result.Fields.Title != "VP Engineering" {
		t.Fatalf("mixed result record = %+v, want the auto-execute half applied and the human name untouched", result.Fields)
	}
	if result.StagedApproval.ApprovalID == "" || len(result.StagedApproval.Fields) != 1 || result.StagedApproval.Fields[0] != "full_name" {
		t.Fatalf("staged_approval = %+v, want exactly full_name staged", result.StagedApproval)
	}

	// Before approval the replay is refused and writes nothing.
	replay := replayWithApprovalID(t, result.StagedApproval.Replay, result.StagedApproval.ApprovalID)
	if _, err := invoke("update_record", replay); !errors.Is(err, apperrors.ErrApprovalTokenInvalid) {
		t.Fatalf("replay before approval → %v, want ErrApprovalTokenInvalid", err)
	}

	// A human approves; the replay redeems and lands exactly the field.
	if status := e.call(t, "POST", "/v1/approvals/"+result.StagedApproval.ApprovalID+"/approve", anyMap{}, nil, nil); status != 200 {
		t.Fatalf("human approve → %d", status)
	}
	if _, err := invoke("update_record", replay); err != nil {
		t.Fatalf("approved replay → %v", err)
	}
	fullName, title := personNameAndTitle(t, invoke, personID)
	if fullName != "Mona Machine" || title != "VP Engineering" {
		t.Fatalf("post-approval record = %q/%q, want the released overwrite", fullName, title)
	}

	// Exactly-once: the second replay finds the approval consumed.
	if _, err := invoke("update_record", replay); !errors.Is(err, apperrors.ErrApprovalTokenInvalid) {
		t.Fatalf("second replay → %v, want ErrApprovalTokenInvalid (consumed)", err)
	}
	// The redeemed write moved ownership: full_name's latest audited
	// writer is now the AGENT, so its next patch of the field is plain
	// auto-execute — precedence protects people, not machines.
	out, err = invoke("update_record", fmt.Sprintf(
		`{"record_type":"person","id":%q,"fields":{"full_name":"Mona Machine II"}}`, personID))
	if err != nil || strings.Contains(string(out), "staged_approval") {
		t.Fatalf("agent re-patch of its own field → %v %s, want a plain auto-execute update", err, out)
	}
}
