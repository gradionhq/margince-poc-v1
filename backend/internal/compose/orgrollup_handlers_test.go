// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"testing"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// TestOrgRollupToWireMapsRestrictedNodes proves the one branch the HTTP
// integration suite cannot reach without inventing a second bounded
// session (the contract has no user-invitation endpoint): a non-empty
// RestrictedExcluded must map into the wire's anonymous item shape by
// value, id and name each carried over exactly. orgRollupToWire is a pure
// function, so this needs no database or HTTP harness at all.
func TestOrgRollupToWireMapsRestrictedNodes(t *testing.T) {
	restrictedID := ids.NewV7()
	result := OrgRollupResult{
		RootID:                 ids.NewV7(),
		Scope:                  orgRollupScopeTree,
		WeightedPipelineMinor:  12_345,
		ClosedWonMinor:         6_789,
		BaseCurrency:           "EUR",
		ActivityCount30d:       3,
		AggregatedAccountCount: 2,
		RestrictedExcluded:     []restrictedNode{{ID: restrictedID, DisplayName: "Hidden Child"}},
		ComputedAt:             time.Date(2026, time.July, 11, 12, 0, 0, 0, time.UTC),
	}

	wire := orgRollupToWire(result)

	if wire.RootId != openapi_types.UUID(result.RootID) || wire.Scope != crmcontracts.OrganizationHierarchyRollupScopeTree {
		t.Errorf("envelope = {root %v scope %v}, want {%v tree}", wire.RootId, wire.Scope, result.RootID)
	}
	if *wire.WeightedPipeline.AmountMinor != 12_345 || *wire.WeightedPipeline.Currency != "EUR" {
		t.Errorf("weighted_pipeline = %+v, want {12345 EUR}", wire.WeightedPipeline)
	}
	if *wire.ClosedWon.AmountMinor != 6_789 || *wire.ClosedWon.Currency != "EUR" {
		t.Errorf("closed_won = %+v, want {6789 EUR}", wire.ClosedWon)
	}
	if len(wire.RestrictedExcluded) != 1 {
		t.Fatalf("restricted_excluded = %+v, want exactly one item", wire.RestrictedExcluded)
	}
	if got := wire.RestrictedExcluded[0]; got.Id != openapi_types.UUID(restrictedID) || got.DisplayName != "Hidden Child" {
		t.Errorf("restricted_excluded[0] = %+v, want {%v Hidden Child}", got, restrictedID)
	}
	if !wire.ComputedAt.Equal(result.ComputedAt) {
		t.Errorf("computed_at = %v, want %v", wire.ComputedAt, result.ComputedAt)
	}
}

// TestOrgRollupToWireEmptyRestrictedIsNeverNil proves the make-with-length
// path also produces a real (non-nil) empty slice when nothing is
// restricted — encoding/json only omits the array shape for a nil slice,
// and the contract requires restricted_excluded to always be present.
func TestOrgRollupToWireEmptyRestrictedIsNeverNil(t *testing.T) {
	result := OrgRollupResult{RootID: ids.NewV7(), Scope: orgRollupScopeSelf, BaseCurrency: "EUR", ComputedAt: time.Now().UTC()}

	wire := orgRollupToWire(result)

	if wire.RestrictedExcluded == nil {
		t.Error("restricted_excluded is nil, want a real empty slice")
	}
	if len(wire.RestrictedExcluded) != 0 {
		t.Errorf("restricted_excluded = %+v, want empty", wire.RestrictedExcluded)
	}
}
