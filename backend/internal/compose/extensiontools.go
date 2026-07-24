// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

// composedTools holds the handler-bearing tools of the composed extension
// set, built once by RegisterExtensions at boot and registered into every
// agents.Registry compose constructs — the same reconcile-at-boot shape a
// jurisdiction pack follows (Register once, consulted by every engine).
// It is written before any registry is built; the mutex guards the
// read/write ordering, not concurrent registrations.
var composedTools struct {
	mu    sync.RWMutex
	tools []mcp.Tool
}

// buildExtensionTools adapts every handler-bearing tool in the composed
// set to the core mcp.Tool seam. A tool without a handler is inert (it
// appears in the manifest but serves nothing), so it is skipped here.
// Tiers and scopes were already grammar-checked by preflightTools; the
// mappings below re-check them so a bad value fails the boot rather than
// registering a mis-tiered tool.
//
// TRUST MODEL: every composed unit's handler-bearing tools are served at
// their declared tier. There is no per-capability operator resolution yet
// (an approvals record binding a decision to each tool's digest is a later
// governance step), so the composed set IS the trust boundary: the vanilla
// tree ships only first-party units, and an installation adds a unit
// deliberately — the same trust a jurisdiction pack rides when it ships
// enabled. A distributed, less-trusted unit is not the model until that
// resolution lands.
func buildExtensionTools(exts []extension.Extension) ([]mcp.Tool, error) {
	var tools []mcp.Tool
	for _, e := range exts {
		for _, tool := range e.Tools {
			if tool.Handle == nil {
				continue
			}
			tier, err := mcpTier(tool.Tier)
			if err != nil {
				return nil, fmt.Errorf("compose: extension %q, tool %q: %w", e.Name, tool.Name, err)
			}
			// A served 🟡 tool would be refused on every call: the admission
			// gate stages a confirm-first approval only for tools that
			// implement the registry's staging seam, which this data-only
			// adapter cannot. Serving one is a dead capability, so reject it
			// until the staging seam is wired. A handler-LESS 🟡 tool is fine
			// — it is a manifest request, not served.
			if tier == mcp.TierConfirmationRequired {
				return nil, fmt.Errorf("compose: extension %q, tool %q: a served confirmation-required tool is not yet supported (its approvals could never be staged)", e.Name, tool.Name)
			}
			scope, err := mcpScope(tool.RequestedScope)
			if err != nil {
				return nil, fmt.Errorf("compose: extension %q, tool %q: %w", e.Name, tool.Name, err)
			}
			input := tool.InputSchema
			if input == nil {
				// MCP requires every tool to advertise an object input schema;
				// a tool that takes no arguments still needs one.
				input = json.RawMessage(`{"type":"object"}`)
			}
			tools = append(tools, extensionTool{
				spec: mcp.ToolSpec{
					Name:          tool.Name,
					Version:       tool.Version,
					RequiredScope: scope,
					Tier:          tier,
					InputSchema:   input,
					OutputSchema:  tool.OutputSchema,
					// A tool requesting the send scope reaches outside the
					// workspace — surface that in the operator inventory.
					Egress: scope == principal.ScopeSend,
				},
				handle: tool.Handle,
			})
		}
	}
	return tools, nil
}

// setComposedTools records the boot's tool set. Called once by
// RegisterExtensions before any registry is built.
func setComposedTools(tools []mcp.Tool) {
	composedTools.mu.Lock()
	defer composedTools.mu.Unlock()
	composedTools.tools = tools
}

// registerComposedTools registers every composed extension tool into a
// freshly built registry, so the MCP transport, the tool listing, and the
// Surface-B runner all serve the same governed set. A tool whose name
// collides with a core tool panics in Register — a genuine boot-time
// wiring conflict, surfaced the same way a duplicate core tool is.
func registerComposedTools(registry *agents.Registry) {
	composedTools.mu.RLock()
	defer composedTools.mu.RUnlock()
	for _, t := range composedTools.tools {
		registry.Register(t)
	}
}

// mcpTier maps a published request tier to the core RiskTier. Only the two
// static tiers are requestable — a dynamic tier needs a resolver, which a
// static declaration cannot carry (extension.Tier.Validate enforces this).
func mcpTier(t extension.Tier) (mcp.RiskTier, error) {
	switch t {
	case extension.TierAutoExecute:
		return mcp.TierAutoExecute, nil
	case extension.TierConfirmationRequired:
		return mcp.TierConfirmationRequired, nil
	}
	return 0, fmt.Errorf("tier %q has no core mapping", string(t))
}

// mcpScope maps a published request scope to the core Passport scope.
func mcpScope(s extension.Scope) (principal.Scope, error) {
	switch s {
	case extension.ScopeRead:
		return principal.ScopeRead, nil
	case extension.ScopeDraft:
		return principal.ScopeDraft, nil
	case extension.ScopeWrite:
		return principal.ScopeWrite, nil
	case extension.ScopeSend:
		return principal.ScopeSend, nil
	case extension.ScopeEnrich:
		return principal.ScopeEnrich, nil
	}
	return "", fmt.Errorf("scope %q has no core mapping", string(s))
}

// extensionTool adapts a published tool declaration to the core mcp.Tool
// seam: the derived spec drives the admission gate exactly as a core
// tool's does, and Handle runs only after admission.
type extensionTool struct {
	spec   mcp.ToolSpec
	handle extension.ToolHandler
}

func (t extensionTool) Spec() mcp.ToolSpec { return t.spec }

func (t extensionTool) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	return t.handle(ctx, in)
}
