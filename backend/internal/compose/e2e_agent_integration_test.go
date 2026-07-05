// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose_test

// End-to-end lane, agent-governance slice: the passport Bearer surface
// (mint → ride → revoke), the ADR-0055 governed agent writes (🟢 lands
// with agent provenance, 🟡 stages an approval a human must decide), and
// the C2 read-seat capability ceiling. Shares setup/env/call with
// e2e_integration_test.go.

import (
	"strings"
	"testing"
)

// The agent path on the REST surface (ADR-0013: agents are clients of the
// same contract): mint a passport over HTTP, then ride it — reads under
// the read scope, writes refused without the write scope, revocation as
// the kill switch.
func TestEndToEnd_passportBearerSurface(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)

	// A human session mints the passport; the response carries the token
	// exactly once.
	var minted struct {
		PassportID string `json:"passport_id"`
		Token      string `json:"token"`
	}
	if status := e.call(t, "POST", "/v1/passports", anyMap{
		"label": "e2e agent", "scopes": []string{"read"},
	}, nil, &minted); status != 201 {
		t.Fatalf("issue passport → %d", status)
	}
	if minted.Token == "" {
		t.Fatal("no token in the mint response")
	}

	bearer := map[string]string{"Authorization": "Bearer " + minted.Token}

	// The read scope reads…
	if status := e.call(t, "GET", "/v1/people", nil, bearer, nil); status != 200 {
		t.Fatalf("bearer GET /people → %d", status)
	}
	// …and cannot write: refused with the scope code, and no row lands.
	var problem struct {
		Code string `json:"code"`
	}
	status := e.call(t, "POST", "/v1/people", anyMap{
		"full_name": "Should not exist", "source": "mcp", "captured_by": "x",
	}, bearer, &problem)
	if status != 403 || problem.Code != "scope_exceeds_grantor" {
		t.Fatalf("read-scope write → %d %q, want 403 scope_exceeds_grantor", status, problem.Code)
	}

	// Bad tokens are 401, not 500.
	if status := e.call(t, "GET", "/v1/people", nil, map[string]string{"Authorization": "Bearer mgp_bogus"}, nil); status != 401 {
		t.Fatalf("bogus bearer → %d", status)
	}

	// Revoke over HTTP (session-authenticated); the token dies with it.
	if status := e.call(t, "DELETE", "/v1/passports/"+minted.PassportID, nil, nil, nil); status != 204 {
		t.Fatalf("revoke → %d", status)
	}
	if status := e.call(t, "GET", "/v1/people", nil, bearer, nil); status != 401 {
		t.Fatalf("revoked bearer still reads: %d", status)
	}
}

// ADR-0055: agent REST writes are governed, not blocked. A write-scoped
// passport's 🟢 mutation lands (with server-stamped agent provenance); a
// 🟡 mutation stages an approval and only a HUMAN decision releases it —
// the agent's own attempt to approve is the self-approval bypass and is
// rejected on principal type; human-only config ops reject the agent
// outright.
func TestEndToEnd_agentWritesGovernedOnREST(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)

	var minted struct {
		Token string `json:"token"`
	}
	if status := e.call(t, "POST", "/v1/passports", anyMap{
		"label": "write agent", "scopes": []string{"read", "write"},
	}, nil, &minted); status != 201 {
		t.Fatalf("issue passport → %d", status)
	}
	bearer := map[string]string{"Authorization": "Bearer " + minted.Token}

	// 🟢 (create_record): the write goes through, and provenance is the
	// authenticated agent — never the body's claim.
	var created struct {
		ID         string `json:"id"`
		CapturedBy string `json:"captured_by"`
	}
	if status := e.call(t, "POST", "/v1/people", anyMap{
		"full_name": "Governed Green Write", "source": "mcp", "captured_by": "human:forged",
	}, bearer, &created); status != 201 {
		t.Fatalf("write-scope 🟢 REST mutation → %d, want 201 (ADR-0055 admits governed agent writes)", status)
	}
	if !strings.HasPrefix(created.CapturedBy, "agent:") {
		t.Fatalf("agent create stamped captured_by=%q, want the authenticated agent", created.CapturedBy)
	}

	// 🟡 (archivePerson is on the confirm-first floor): no effect, a
	// staged approval instead.
	var problem struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}
	status := e.call(t, "DELETE", "/v1/people/"+created.ID, nil, bearer, &problem)
	if status != 403 || problem.Code != "approval_required" {
		t.Fatalf("🟡 REST mutation → %d %q, want 403 approval_required", status, problem.Code)
	}
	if getStatus := e.call(t, "GET", "/v1/people/"+created.ID, nil, bearer, nil); getStatus != 200 {
		t.Fatalf("staged archive must not have executed; GET → %d", getStatus)
	}
	approvalID := extractStagedApprovalID(t, problem.Detail)

	// The agent may STAGE but never APPROVE — including its own staging.
	var denyBody struct {
		Code string `json:"code"`
	}
	if status := e.call(t, "POST", "/v1/approvals/"+approvalID+"/approve", anyMap{}, bearer, &denyBody); status != 403 || denyBody.Code != "permission_denied" {
		t.Fatalf("agent self-approval → %d %q, want 403 permission_denied", status, denyBody.Code)
	}

	// Human-only config surface rejects the agent whatever its scopes.
	if status := e.call(t, "POST", "/v1/pipelines", anyMap{"name": "Shadow"}, bearer, &denyBody); status != 403 || denyBody.Code != "permission_denied" {
		t.Fatalf("agent on human-only pipeline config → %d %q, want 403 permission_denied", status, denyBody.Code)
	}

	// A human approves; the agent repeats the IDENTICAL request with the
	// approval token and the effect lands exactly once.
	if status := e.call(t, "POST", "/v1/approvals/"+approvalID+"/approve", anyMap{}, nil, nil); status != 200 {
		t.Fatalf("human approve → %d", status)
	}
	withToken := map[string]string{"Authorization": "Bearer " + minted.Token, "X-Approval-Token": approvalID}
	if status := e.call(t, "DELETE", "/v1/people/"+created.ID, nil, withToken, nil); status != 200 {
		t.Fatalf("approved retry → %d, want the archive to execute", status)
	}
	// Single-use: the same token cannot authorize a second effect.
	if status := e.call(t, "DELETE", "/v1/people/"+created.ID, nil, withToken, &problem); status == 200 {
		t.Fatal("a consumed approval token authorized a second effect")
	}
}

// extractStagedApprovalID pulls the staged approval's id out of the 403
// approval_required detail — the same reference the human inbox lists.
func extractStagedApprovalID(t *testing.T, detail string) string {
	t.Helper()
	const marker = "staged as approval "
	i := strings.Index(detail, marker)
	if i < 0 {
		t.Fatalf("no staged approval reference in %q", detail)
	}
	rest := detail[i+len(marker):]
	if j := strings.IndexByte(rest, ' '); j > 0 {
		rest = rest[:j]
	}
	return rest
}

// C2: a read seat is a hard capability ceiling — a read-seat human may read
// but not mutate over REST, whatever their role grants (A62/ADR-0047). The
// bootstrap admin is a full seat that mutates; flipping the workspace to
// read seats turns the same authenticated call into a 403.
func TestEndToEnd_readSeatCannotMutate(t *testing.T) {
	e := setup(t)
	e.bootstrapWorkspace(t)

	// A full-seat admin creates freely.
	var created struct {
		ID string `json:"id"`
	}
	if status := e.call(t, "POST", "/v1/people", anyMap{
		"full_name": "Full Seat Made", "source": "manual", "captured_by": "admin",
	}, nil, &created); status != 201 {
		t.Fatalf("full-seat create → %d", status)
	}

	// Demote to a read seat; the live seat is read at authentication, so the
	// same session now hits the ceiling.
	e.setWorkspaceSeat(t, e.slug, "read")

	// Reads still succeed…
	if status := e.call(t, "GET", "/v1/people", nil, nil, nil); status != 200 {
		t.Fatalf("read-seat GET → %d", status)
	}
	// …every mutation is refused with the seat code, before RBAC.
	var problem struct {
		Code string `json:"code"`
	}
	if status := e.call(t, "POST", "/v1/people", anyMap{
		"full_name": "Read Seat Blocked", "source": "manual", "captured_by": "admin",
	}, nil, &problem); status != 403 || problem.Code != "seat_tier_insufficient" {
		t.Fatalf("read-seat create → %d %q, want 403 seat_tier_insufficient", status, problem.Code)
	}
	if status := e.call(t, "PATCH", "/v1/people/"+created.ID, anyMap{"title": "X"}, nil, &problem); status != 403 || problem.Code != "seat_tier_insufficient" {
		t.Fatalf("read-seat update → %d %q, want 403 seat_tier_insufficient", status, problem.Code)
	}
}
