// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// The eight bespoke commands' half of the governance seam
// (modules/agents/commandsidecar.go, commandaction.go), proved the same way
// agentcommandtarget_integration_test.go proves it for archive: the staged
// approval ROW.
//
// The defect this task exists to prevent: every fact on one organization
// rides the identical update_record verb, so a design that projected the
// call onto that verb's own arguments would let one approval redeem any of
// them. The proof has to be two DIFFERENT fact keys on the SAME organization
// staging two DISTINGUISHABLE approvals — different diff_hash, different
// summary — asserted on the rows, not on the ErrRequiresApproval sentinel a
// refusal with nowhere to land would answer just as readily.

import (
	"net/http"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
)

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
