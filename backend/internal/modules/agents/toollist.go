// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agents

// What a caller is SHOWN: the tool list tools/list answers, filtered to what
// this principal could invoke at all, and the description each entry carries.
//
// It sits beside the dispatcher rather than inside it because advertising and
// dispatching answer different questions. This decides what a client is told
// exists; the gate decides what it may do. The two must agree without being the
// same code — a surface that advertises what the gate will refuse is a surface
// that lies, and the client's only way to learn the truth is to call and be
// denied.

import (
	"context"
	"fmt"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// invocableByCaller reports whether the calling principal's passport scopes
// would let it invoke spec at all. It mirrors the scope arm of auth.Gate.Admit
// deliberately — a surface that advertises what the gate will refuse is a
// surface that lies, and the client's only way to discover the truth is to
// call and be denied.
//
// Registry.Offered is its only caller, and every narrowing of the TOOL catalog
// goes through there — the external tools/list and the tool listing a Surface-B
// run is shown alike.
//
// The resources catalogue is the one sibling that does not: readableByCaller
// spells the same three branches over mcp.Resource. One rule, two spellings,
// because neither type carries the other's fields — which is a real cost, not a
// tidy separation. A third surface needing this rule should lift the predicate
// rather than copy it a third time.
//
// It answers the SCOPE axis only, which is what §5.7 promises. The seat
// ceiling and object RBAC are re-derived per call through the authority seam
// and are a named follow-up (§10.2); this filter must not pretend to enforce
// them, and Registry.Invoke remains the authority for every one of them.
//
// A ctx with no principal shows nothing rather than everything: the caller of
// a tools/list that never authenticated has no scopes, and an empty surface is
// the honest answer.
func invocableByCaller(ctx context.Context, spec mcp.ToolSpec) bool {
	p, ok := principal.Actor(ctx)
	if !ok {
		return false
	}
	// Humans and the system principal do not ride the scope model — their
	// authority is their RBAC, enforced at the store — so filtering them by a
	// passport scope they never carry would hide the whole surface.
	if p.Type != principal.PrincipalAgent {
		return true
	}
	return p.Scopes.Has(spec.RequiredScope)
}

// DescribeForClient is the description one tool is advertised with: what the
// tool is FOR, written on its spec, followed by how this server will govern the
// call. It is exported because tools/list is not the only surface that serves
// it — the operator console reads the same text through GET /v1/agent-tools,
// and a second rendering there would be a second answer to what a client is
// told.
//
// The order is the point. The written text answers the question a model is
// actually asking — which of thirty tools serves this goal — and the governance
// clause answers what happens once it has chosen. A description carrying only
// the second tells a model the passport scope of every tool and the purpose of
// none.
//
// The tier and scope are re-stated from the spec the admission gate enforces,
// so they cannot disagree with it. The crm.yaml operation family is NOT here:
// it is developer documentation, and a model has no use for the name of an
// endpoint it has no way to call. It stays on ToolSpec.OpenAPIOp, which is what
// the contract-parity gate reads.
func DescribeForClient(spec mcp.ToolSpec) string {
	// Every arm is named, and the fallthrough is the CONSERVATIVE reading, not
	// the convenient one: the admission gate treats anything that is not
	// TierAutoExecute as confirm-first, so a tier added without updating this
	// switch must not be advertised as running unattended. The same posture
	// tierWire takes on the REST side, for the same reason.
	tier := "a person approves every call before it runs"
	switch spec.Tier {
	case mcp.TierAutoExecute:
		tier = "runs immediately"
	case mcp.TierConfirmationRequired:
		tier = "a person approves every call before it runs"
	case mcp.TierDynamic:
		tier = "some calls run immediately and others a person approves first, decided per call from its arguments"
	}
	return fmt.Sprintf("%s (Governance: %s; requires passport scope %q.)", spec.Description, tier, spec.RequiredScope)
}

// toolList reads Registry.Offered rather than filtering Specs itself, so the
// external catalog and the one a Surface-B run is shown are the same function
// and not two that agree today.
//
// It takes the FRAMING because one member of a tool's entry is negotiated:
// `_meta.ui` is offered only to a request that declared the App extension, and
// only where this server actually serves views. That is the rule the Tasks
// extension already rides, for the same reason — advertising an extension to a
// client that cannot enter its negotiation offers a capability the client has no
// way to use, and here one whose whole point is that the HOST prefetch and
// sandbox a document it was told about.
func (s *Dispatcher) toolList(ctx context.Context, fr framing) []map[string]any {
	specs := s.registry.Offered(ctx)
	tools := make([]map[string]any, 0, len(specs))
	for _, spec := range specs {
		tool := map[string]any{
			fieldName: spec.Name,
			// Top-level title outranks annotations.title for display, and both
			// outrank the name. Registry.Register refuses a title-less tool, so
			// neither is ever the empty string here.
			fieldTitle:    spec.Title,
			"description": DescribeForClient(spec),
			"inputSchema": spec.InputSchema,
			// The two hints this server can state as FACTS, both read off the
			// spec the admission gate itself enforces rather than restated by
			// hand: what a tool may change is its scope, and whether it leaves
			// the workspace is its egress flag.
			//
			// destructiveHint and idempotentHint are deliberately absent: their
			// protocol defaults (destructive, non-idempotent) are already the
			// conservative reading, and only the looser value would need a
			// per-tool judgement, with nothing to hold it true.
			"annotations": map[string]any{
				fieldTitle:      spec.Title,
				"readOnlyHint":  spec.ReadOnly(),
				"openWorldHint": spec.Egress,
			},
		}
		if spec.OutputSchema != nil {
			tool["outputSchema"] = spec.OutputSchema
		}
		// The view, offered only where BOTH halves are real: this request
		// declared the extension, and this server serves the documents. Either
		// missing and the member is absent rather than empty — a client reads
		// `_meta.ui` as "there is a view to fetch", so an entry pointing at a
		// document this deployment does not publish is worse than none.
		if s.appsOffered(fr) {
			if ui := toolUIMeta(spec); ui != nil {
				tool[fieldMeta] = map[string]any{metaUIKey: ui}
			}
		}
		tools = append(tools, tool)
	}
	return tools
}
