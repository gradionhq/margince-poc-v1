// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package automation

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// TestResolveAssignOwnerTierSingleEntityIsGreen is the common case every
// shipped automation hits today: one Action, one Target, Bulk unset.
func TestResolveAssignOwnerTierSingleEntityIsGreen(t *testing.T) {
	got := resolveAssignOwnerTier(AssignOwnerScope{Bulk: false})
	if got != mcp.TierGreen {
		t.Errorf("resolveAssignOwnerTier(single-entity) = %v, want TierGreen", got)
	}
}

// TestResolveAssignOwnerTierAtScaleIsYellow proves the escalation branch
// against a synthetic scaled input: no shipped automation sets Bulk yet
// (AssignOwnerScope's doc), but the 🟡 branch must still be proven, or an
// untested escalation path is exactly the risk AUTO-T07's dynamic tier
// exists to prevent.
func TestResolveAssignOwnerTierAtScaleIsYellow(t *testing.T) {
	got := resolveAssignOwnerTier(AssignOwnerScope{Bulk: true})
	if got != mcp.TierYellow {
		t.Errorf("resolveAssignOwnerTier(at-scale) = %v, want TierYellow", got)
	}
}
