// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The eight bespoke commands' half of the governance seam
// (modules/agents/commandsidecar.go, commandaction.go), proved the same way
// agentcommandtarget_integration_test.go proves it for archive: the staged
// approval ROW.
//
// TestAConfirmFirstFactCallAgainstAnUnseeableOrganizationStagesNothing below
// is this task's actual proof: registering these eight in restCommands
// makes Guards run before anything stages, where the route-walk fallback
// (stagedTargetByRoute) ran none. TestTwoFactKeysOnOneOrganizationStageDistinguishableApprovals
// does NOT prove that — diff_hash and summary are both derived from the
// concrete request path upstream of the resolver (canonicalRESTCall,
// restSummary), so it would pass unchanged against the pre-task route walk
// too. It stays because it is the one place production's chi parameter
// names (factKey, field, person_id) are exercised through the REAL router
// rather than hand-bound in a unit test's route context.

import (
	"net/http"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// TestAConfirmFirstFactCallAgainstAnUnseeableOrganizationStagesNothing is
// this task's actual behavioral proof: an agent acting for a human whose row
// scope hides an organization (team-scoped, no shared team with the
// organization's owner) gets the existence-hiding 404 GetOrganization itself
// would give, and stages NO approval. Without this task's restCommands entry,
// stagedTargetByRoute answers the target from the route alone — no read, no
// refusal — and stages one regardless; that is the entire delta this test
// exercises through the REAL row-scope predicate (internal/platform/auth),
// which TestAnOperandCommandOfAnUnseeableRecordStagesNothing (agentcommandoperand_test.go)
// can only fake.
func TestAConfirmFirstFactCallAgainstAnUnseeableOrganizationStagesNothing(t *testing.T) {
	e := apptest.SetupApp(t)
	e.BootstrapWorkspace(t)

	var adminID string
	if err := e.Owner.QueryRow(t.Context(), `SELECT id FROM app_user WHERE email = 'ada@example.com'`).Scan(&adminID); err != nil {
		t.Fatalf("resolving admin id: %v", err)
	}

	// Owned by the admin: owner_id IS NULL is visible to everyone regardless
	// of row scope, so an explicit owner the rep below shares no team with is
	// what makes the miss genuine rather than incidental.
	orgID := createdID(t, e, "/v1/organizations", apptest.AnyMap{
		"display_name": "Row-Scope Hidden Inc", "owner_id": adminID,
	})

	wsA := wsID(t, e, e.Slug)
	rep := ids.NewV7()
	seedInWorkspace(t, e, wsA,
		stmt(`INSERT INTO app_user (id, workspace_id, email, display_name) VALUES ($1, $2, 'rep@example.com', 'Rep One')`, rep, wsA),
		// Borrow the bootstrap admin's hash so the rep can actually sign in —
		// the same reason TestRosterWithholdsRoleKeysFromANonAdmin does.
		stmt(`UPDATE app_user SET password_hash = (SELECT password_hash FROM app_user WHERE email = 'ada@example.com') WHERE id = $1`, rep),
		// 'rep' is RowScopeTeam with no team_membership row seeded — an empty
		// team set, so the predicate's team arm matches nothing and only
		// owner_id = rep's own id could pass it, which it does not.
		stmt(`INSERT INTO role_assignment (workspace_id, role_id, user_id) SELECT $2, r.id, $1 FROM role r WHERE r.key = 'rep'`, rep, wsA),
	)
	if status := e.Call(t, "POST", "/v1/auth/login", apptest.AnyMap{
		"email": "rep@example.com", "password": "correct-horse-battery",
	}, nil, nil); status != http.StatusOK {
		t.Fatalf("rep login → %d, want 200", status)
	}
	bearer := agentBearer(t, e, "row-scope-miss agent")

	var problem struct {
		Code string `json:"code"`
	}
	status := e.Call(t, "POST", "/v1/organizations/"+orgID+"/facts/named_customer:acme-inc/confirm", nil, bearer, &problem)
	if status != http.StatusNotFound {
		t.Fatalf("confirming a fact on an organization the acting human cannot see answered %d %q, want 404 — "+
			"the refusal must not tell a caller that a row they may not see exists", status, problem.Code)
	}

	var n int
	if err := e.Owner.QueryRow(t.Context(), `SELECT count(*) FROM approval WHERE target_entity_id = $1`, orgID).Scan(&n); err != nil {
		t.Fatalf("counting approvals: %v", err)
	}
	if n != 0 {
		t.Errorf("staged %d approvals against an organization nobody could ever decide about, want 0", n)
	}
}

func TestTwoFactKeysOnOneOrganizationStageDistinguishableApprovals(t *testing.T) {
	e := apptest.SetupApp(t)
	e.BootstrapWorkspace(t)
	bearer := agentBearer(t, e, "fact-confirm agent")

	orgID := createdID(t, e, "/v1/organizations", apptest.AnyMap{"display_name": "Distinguishable Facts Inc"})

	approvalA := stageFactConfirm(t, e, bearer, orgID, "named_customer:acme-inc")
	approvalB := stageFactConfirm(t, e, bearer, orgID, "annual_revenue:2026")

	typeA, idA, hashA, summaryA := readApproval(t, e, approvalA)
	typeB, idB, hashB, summaryB := readApproval(t, e, approvalB)

	if typeA != "organization" || typeB != "organization" {
		t.Errorf("staged target types = (%q,%q), want both \"organization\"", typeA, typeB)
	}
	if idA == nil || idB == nil {
		t.Fatal("a staged approval names no target id — a decision about which organization was never captured")
	}
	if *idA != orgID || *idB != orgID {
		t.Fatalf("staged target ids = (%s,%s), want both %s — both facts belong to the SAME organization, so a "+
			"target id alone cannot be what tells the two apart", *idA, *idB, orgID)
	}
	if hashA == hashB {
		t.Error("two different fact keys on the same organization staged the SAME diff_hash — one approval " +
			"could redeem either")
	}
	if summaryA == summaryB {
		t.Error("two different fact keys on the same organization staged the SAME summary — a human triaging " +
			"the inbox cannot tell which fact they are confirming")
	}
}

// stageFactConfirm provokes a refused agent fact confirmation and returns
// the approval id it staged.
func stageFactConfirm(t *testing.T, e *apptest.AppEnv, bearer map[string]string, orgID, factKey string) string {
	t.Helper()
	var problem struct {
		Code   string `json:"code"`
		Detail string `json:"detail"`
	}
	path := "/v1/organizations/" + orgID + "/facts/" + factKey + "/confirm"
	if status := e.Call(t, "POST", path, nil, bearer, &problem); status != http.StatusForbidden ||
		problem.Code != "approval_required" {
		t.Fatalf("agent fact confirm %q → %d %q, want 403 approval_required", factKey, status, problem.Code)
	}
	return ExtractStagedApprovalID(t, problem.Detail)
}

// readApproval reads the staged row's target and the two fields that must
// distinguish it from a sibling staging: diff_hash and summary.
func readApproval(t *testing.T, e *apptest.AppEnv, approvalID string) (targetType string, targetID *string, diffHash, summary string) {
	t.Helper()
	if err := e.Owner.QueryRow(t.Context(),
		`SELECT coalesce(target_entity_type, ''), target_entity_id, diff_hash, coalesce(summary, '') FROM approval WHERE id = $1`,
		approvalID).Scan(&targetType, &targetID, &diffHash, &summary); err != nil {
		t.Fatalf("reading approval %s: %v", approvalID, err)
	}
	return targetType, targetID, diffHash, summary
}
