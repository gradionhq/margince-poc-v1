// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package automation

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// TestResolveAssignOwnerTierSingleEntityIsAutoExecute is the common case every
// shipped automation hits today: one Action, one Target, Bulk unset.
func TestResolveAssignOwnerTierSingleEntityIsAutoExecute(t *testing.T) {
	got := resolveAssignOwnerTier(AssignOwnerScope{Bulk: false})
	if got != mcp.TierAutoExecute {
		t.Errorf("resolveAssignOwnerTier(single-entity) = %v, want TierAutoExecute", got)
	}
}

// TestResolveAssignOwnerTierAtScaleIsConfirmationRequired proves the escalation branch
// against a synthetic scaled input: no shipped automation sets Bulk yet
// (AssignOwnerScope's doc), but the 🟡 branch must still be proven, or an
// untested escalation path is exactly the risk AUTO-T07's dynamic tier
// exists to prevent.
func TestResolveAssignOwnerTierAtScaleIsConfirmationRequired(t *testing.T) {
	got := resolveAssignOwnerTier(AssignOwnerScope{Bulk: true})
	if got != mcp.TierConfirmationRequired {
		t.Errorf("resolveAssignOwnerTier(at-scale) = %v, want TierConfirmationRequired", got)
	}
}
