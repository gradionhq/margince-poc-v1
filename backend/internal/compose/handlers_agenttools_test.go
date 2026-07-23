// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

func TestTierWireIsExhaustive(t *testing.T) {
	cases := map[mcp.RiskTier]crmcontracts.AgentToolTier{
		mcp.TierAutoExecute:          crmcontracts.AgentToolTierAutoExecute,
		mcp.TierConfirmationRequired: crmcontracts.AgentToolTierConfirmationRequired,
		mcp.TierDynamic:              crmcontracts.AgentToolTierDynamic,
	}
	for in, want := range cases {
		if got := tierWire(in); got != want {
			t.Fatalf("tierWire(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestAgentToolsMapPreservesRegistryOrderAndFields(t *testing.T) {
	specs := []mcp.ToolSpec{
		{Name: "b_tool", OpenAPIOp: "send_email", RequiredScope: "send", Tier: mcp.TierConfirmationRequired, Egress: true},
		{Name: "a_tool", OpenAPIOp: "search_records", RequiredScope: "read", Tier: mcp.TierAutoExecute},
	}
	got := agentToolsFromSpecs(specs)
	if len(got) != 2 || got[0].Name != "b_tool" || !got[0].Egress {
		t.Fatalf("mapping dropped fields or reordered: %+v", got)
	}
}
