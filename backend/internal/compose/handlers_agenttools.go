// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"net/http"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// ListAgentTools exposes the composed tool registry to the operator UI. It is a
// compose-level method (not a module handler) because the registry is a
// cross-module composition artifact — no single module owns the full surface.
func (s Server) ListAgentTools(w http.ResponseWriter, _ *http.Request) {
	body := crmcontracts.AgentToolListResponse{Data: agentToolsFromSpecs(s.toolRegistry.Specs())}
	httperr.WriteJSON(w, http.StatusOK, body)
}

func agentToolsFromSpecs(specs []mcp.ToolSpec) []crmcontracts.AgentTool {
	out := make([]crmcontracts.AgentTool, 0, len(specs))
	for _, spec := range specs {
		out = append(out, crmcontracts.AgentTool{
			// Name doubles as the action verb in this registry (e.g.
			// "search_records", "send_email"); OpenAPIOp is the
			// underlying REST operationId/family the tool maps to, not
			// the verb.
			Name:          spec.Name,
			RequiredScope: ptrString(string(spec.RequiredScope)),
			Tier:          tierWire(spec.Tier),
			Egress:        spec.Egress,
		})
	}
	return out
}

// tierWire maps the closed AutoExecute/ConfirmationRequired/Dynamic RiskTier set (the only
// tiers RiskTier's definition currently admits) onto the wire enum. The
// fallthrough below is a conservative ConfirmationRequired floor for the unreachable
// case, not a fitness guarantee: TestTierWireIsExhaustive only checks
// today's three known tiers and would not catch a new 4th RiskTier value
// going unhandled here — adding one requires updating this switch by hand.
func tierWire(t mcp.RiskTier) crmcontracts.AgentToolTier {
	switch t {
	case mcp.TierAutoExecute:
		return crmcontracts.AgentToolTierAutoExecute
	case mcp.TierConfirmationRequired:
		return crmcontracts.AgentToolTierConfirmationRequired
	case mcp.TierDynamic:
		return crmcontracts.AgentToolTierDynamic
	}
	return crmcontracts.AgentToolTierConfirmationRequired // unreachable; conservative floor if a tier is added without updating this switch
}

func ptrString(v string) *string { return &v }
