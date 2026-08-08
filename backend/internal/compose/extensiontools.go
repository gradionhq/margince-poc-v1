// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
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
func buildExtensionTools(exts []extension.Extension, verbs []extension.Verb) ([]mcp.Tool, error) {
	// The declaration side, keyed by (unit, verb). A unit's Go Tools entry is
	// only a verb and a function now; everything the adapted spec carries —
	// tier, scope, prose, schemas, version — comes from the contract-declared
	// operation, re-emitted into the composition as a literal.
	declared := make(map[string]extension.Verb, len(verbs))
	for _, v := range verbs {
		if err := v.Validate(); err != nil {
			return nil, fmt.Errorf("compose: extension %q: %w", v.Unit, err)
		}
		key := verbKey(v.Unit, v.Tool)
		if prior, dup := declared[key]; dup {
			return nil, fmt.Errorf("compose: extension %q declares tool %q on both %s %s and %s %s — one verb, one operation",
				v.Unit, v.Tool, prior.Method, prior.Route, v.Method, v.Route)
		}
		declared[key] = v
	}
	var tools []mcp.Tool
	// preflightTools rejects a name declared twice WITHIN a unit; the tool
	// registry's namespace is global, so a name two units both serve would
	// otherwise pass validation and only surface as a Register panic after
	// jurisdictions are already applied. Reject it here, in the pre-apply
	// phase, so the boot stays validate-then-apply.
	served := make(map[string]extension.Name)
	for _, e := range exts {
		for _, tool := range e.Tools {
			if tool.Handle == nil {
				continue
			}
			if owner, dup := served[tool.Name]; dup {
				return nil, fmt.Errorf("compose: extensions %q and %q both serve a tool named %q", owner, e.Name, tool.Name)
			}
			served[tool.Name] = e.Name
			verb, ok := declared[verbKey(e.Name, tool.Name)]
			if !ok {
				// Behavior with no published surface. The generator already
				// refuses this at the declaration's own line, so reaching it
				// here means the composed set was assembled outside that path —
				// which is exactly when a fail-closed boot matters.
				return nil, fmt.Errorf("compose: extension %q serves tool %q but no operation in its contract fragments declares it", e.Name, tool.Name)
			}
			adapted, err := adaptExtensionTool(e.Name, tool, verb)
			if err != nil {
				return nil, fmt.Errorf("compose: extension %q, tool %q: %w", e.Name, tool.Name, err)
			}
			tools = append(tools, adapted)
		}
	}
	return tools, nil
}

// adaptExtensionTool maps ONE handler-bearing declaration onto the core
// seam, refusing the shapes this surface cannot honestly serve. unit is the
// declaring extension's name, carried onto the adapted tool because it is
// what the per-call Runtime is scoped to — the composed declaration is the
// only place that fact exists, and the handler must never be able to supply
// it.
func adaptExtensionTool(unit extension.Name, tool extension.Tool, verb extension.Verb) (extensionTool, error) {
	tier, err := mcpTier(verb.Tier)
	if err != nil {
		return extensionTool{}, err
	}
	// A served 🟡 tool would be refused on every call: the admission gate
	// stages a confirm-first approval only for tools that implement the
	// registry's staging seam, which this data-only adapter cannot. Serving
	// one is a dead capability, so reject it until the staging seam is wired.
	// A handler-LESS 🟡 tool is fine — it is a manifest request, not served.
	if tier == mcp.TierConfirmationRequired {
		return extensionTool{}, errors.New("a served confirmation-required tool is not yet supported (its approvals could never be staged)")
	}
	scope, err := mcpScope(verb.RequestedScope)
	if err != nil {
		return extensionTool{}, err
	}
	// Every core tool that leaves the workspace — send_email, send_message,
	// book_meeting, the enrich pair — is 🟡, because the only control over a
	// destination the product did not choose is a human deciding the call. A
	// served extension tool cannot be 🟡 (see just above), so serving an
	// outbound one would put this surface on the wrong side of that rule:
	// outbound authority nothing declares, on a call nobody can be asked
	// about. A handler-LESS outbound tool is fine — it is a manifest request,
	// not a capability.
	//
	// This binds the DECLARATION. A handler is ordinary Go and could reach the
	// network whatever cap it asks for — that is bounded by the composed set
	// being the trust boundary (see buildExtensionTools), not by this check.
	// What the check buys is that a unit cannot ASK for outbound authority and
	// be granted it silently on the auto-execute tier.
	if scope.Egresses() {
		return extensionTool{}, fmt.Errorf(
			"a served tool spending the outbound %q cap is not yet supported "+
				"(leaving the workspace requires the confirm-first tier, which this surface cannot stage)", scope)
	}
	// LAST of the refusals, because the two above are about what this surface
	// can HONESTLY serve and this one is about what a caller is told. A served
	// tool with no description is one a model can select only by the shape of
	// its verb, listed beside thirty core tools that each say what they are
	// for. Unlike Title there is no honest fallback — the string it
	// would fall back to is the name, which is what a description explains — so
	// this is refused rather than defaulted, and refused HERE, in the pre-apply
	// phase where the unit and the tool are both named, rather than as the core
	// registry's boot panic. A handler-LESS tool is untouched: it is a manifest
	// request no client is ever shown.
	if strings.TrimSpace(verb.Description) == "" {
		return extensionTool{}, errors.New("a served tool declares no Description — the text a model selects it by, " +
			"which nothing about the tool can be derived from")
	}
	// And its result contract's version, in the same phase and for the same
	// shape of reason: every result this surface seals carries it as
	// `schema_version`, and a unit declaring none would tell every client that
	// its result shape can never be compared against a later one. The version
	// is the CONTRACT's now, like the description above — Verb.Validate
	// already refuses an empty one at gen time, and this restates it at the
	// serving side so a composed set assembled outside that path fails here,
	// named, rather than as the core registry's Register panic.
	if strings.TrimSpace(verb.Version) == "" {
		return extensionTool{}, errors.New("a served tool declares no Version — every result carries it as " +
			"schema_version, which is what lets a client tell a changed shape from changed data")
	}
	input := verb.InputSchema
	if input == nil {
		// MCP requires every tool to advertise an object input schema; a tool
		// that takes no arguments still needs one.
		input = json.RawMessage(`{"type":"object"}`)
	}
	// The object and the action are carried onto the adapted tool for the same
	// reason unit is: the composed declaration is the only place they exist,
	// and mcp.ToolSpec has no field for either — the core gate's model is
	// scope ∧ seat ∧ tier ∧ quota, and object-level RBAC is enforced by the
	// handler at every core store rather than by the gate. So this adapter is
	// where an extension's declared grant becomes a live check; see Handle.
	return extensionTool{
		rbacObject: verb.RbacObject,
		rbacAction: verb.RbacAction,
		spec: mcp.ToolSpec{
			Name: tool.Name,
			// A unit that declares a title gets it; one that does not is
			// listed under its verb, which is what a client falls back to
			// anyway. Optional rather than required on purpose: making a
			// display string mandatory would refuse to boot an otherwise valid
			// third-party unit over a label.
			Title: cmp.Or(verb.Title, tool.Name),
			// Required above, so it is never the empty string a client would
			// render as an undescribed tool.
			Description:   verb.Description,
			Version:       verb.Version,
			RequiredScope: scope,
			Tier:          tier,
			InputSchema:   input,
			OutputSchema:  verb.OutputSchema,
			// Derived, never declared: egress is a property of the cap spent,
			// not something a unit asserts. The refusal above means it is
			// false for everything this surface serves today.
			Egress: scope.Egresses(),
		},
		unit:   string(unit),
		handle: tool.Handle,
	}, nil
}

// verbKey pairs a unit with one of its verbs. A tool name is unique within a
// unit but the registry's namespace is global, so the JOIN between behavior and
// declaration has to be per unit — otherwise one unit's contract could supply
// the governance for another unit's handler.
func verbKey(unit extension.Name, tool string) string {
	return string(unit) + "\x00" + tool
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
// Surface-B runner all serve the same governed set. Extension-vs-extension
// name collisions are already rejected in buildExtensionTools; an extension
// tool whose name collides with a CORE tool still panics in Register — a
// genuine boot-time wiring conflict, surfaced the same way a duplicate core
// tool is.
func registerComposedTools(registry *agents.Registry) {
	composedTools.mu.RLock()
	defer composedTools.mu.RUnlock()
	for _, t := range composedTools.tools {
		registry.Register(t)
	}
}

// composedToolNames names the extension tools this boot registered. The
// contract-parity sweeps use it to tell the third legitimate kind of registered
// verb — one a unit manifest declares — from a core verb nothing declares.
func composedToolNames() map[string]bool {
	composedTools.mu.RLock()
	defer composedTools.mu.RUnlock()
	names := make(map[string]bool, len(composedTools.tools))
	for _, t := range composedTools.tools {
		names[t.Spec().Name] = true
	}
	return names
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
	spec mcp.ToolSpec
	// unit is the declaring extension's name, the scope of every Runtime
	// this tool's handler is invoked with.
	unit string
	// rbacObject and rbacAction are the grant the contract declares this
	// operation needs, or both empty for a tool that owns no records.
	rbacObject string
	rbacAction extension.RbacAction
	handle     extension.ToolHandler
}

func (t extensionTool) Spec() mcp.ToolSpec { return t.spec }

// Handle mints the call-scoped Runtime, runs the handler with it, and
// releases it — which is the design's central mechanism, and the reason it
// is HERE and not in the unit's constructor. A declaration holds no handle,
// so nothing an extension can reach exists until admission has already
// happened and this line runs; and because the release is deferred rather
// than left to the handler, a retained Runtime is a reported failure
// (extension.ErrRuntimeExpired) rather than a live capability that outlived
// the call it was granted for.
func (t extensionTool) Handle(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
	// Object-level RBAC, and this is the ONE place it can be: the core gate
	// decides scope ∧ seat ∧ tier ∧ quota and knows nothing about objects, so
	// without this line a declared x-rbac-object would register into the
	// vocabulary, reach /me, and gate nothing — a screen would hide a control
	// the same principal could still reach through the agent.
	//
	// HERE rather than in the mounted route (extroutes.go), because this is
	// where the two surfaces converge. The REST route and an MCP tools/call
	// both arrive through Registry.Invoke, so a check in the route would leave
	// the agent path open; and Invoke has already re-derived the granting
	// human's live RBAC onto the context by the time it calls this, so the
	// grant read below is the current one rather than whatever the transport
	// stamped at session start.
	//
	// It runs BEFORE the Runtime is minted: a principal who may not touch the
	// records must not reach a live capability handle, even briefly.
	if t.rbacObject != "" {
		if err := auth.Require(ctx, t.rbacObject, principal.Action(t.rbacAction)); err != nil {
			return nil, err
		}
	}
	pool, vault := boundExtensionRuntime()
	// ctx here is the INVOCATION's — the one the admission gate ran against —
	// and the Runtime keeps it, so every capability re-derives the tenant from
	// it rather than from whatever context the handler later passes back in.
	rt := runtimeFor(ctx, t.unit, pool, vault)
	// Deferred, not called on the return path: a handler that panics has
	// still finished with its Runtime, and a panic recovered upstream must
	// not leave a live one behind.
	defer rt.release()
	return t.handle(ctx, rt, in)
}
