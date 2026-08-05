// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// One verb, one cap, both wires. The contract's x-mcp-tool annotation names
// the passport scope a REST call spends; the registered tool names the scope
// the same verb spends over MCP. If those two ever disagree, a passport
// refused the verb on one surface can spend it on the other — the cap the
// granting human set would depend on which transport the agent happened to
// pick.
//
// Both sweeps are derived from the generated policy table, so a verb added to
// the contract tomorrow is covered without anyone extending a list.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// grantableScopes is the closed passport vocabulary (interfaces.md §2), the
// same set identity admits a mint against. A contract scope outside it would
// make the gate demand a cap no passport can hold: fail-closed, but silently
// so — the verb would simply never be admissible, and nothing would say why.
var grantableScopes = map[principal.Scope]bool{
	principal.ScopeRead:   true,
	principal.ScopeDraft:  true,
	principal.ScopeWrite:  true,
	principal.ScopeSend:   true,
	principal.ScopeEnrich: true,
}

func TestTheContractScopeMatchesTheRegisteredToolScope(t *testing.T) {
	registry := NewRegistry(nil, SendPath{})

	compared := 0
	for route, pol := range agentPolicies {
		if pol.Access != accessTool {
			continue
		}
		spec, registered := registry.Spec(pol.Tool)
		if !registered {
			// A synthesized verb has no second opinion to disagree with; its
			// pin lives in agentpolicysynthesis_test.go.
			continue
		}
		compared++
		if want := principal.Scope(pol.Scope); spec.RequiredScope != want {
			t.Errorf("%s (%s): the contract declares scope %q but the registered tool requires %q — "+
				"the same verb would spend a different cap on REST than on MCP. Decide which cap the "+
				"act actually spends, then change the losing side: api/crm.yaml's x-mcp-tool scope, or "+
				"the tool's RequiredScope in its module registration.",
				pol.Tool, route, want, spec.RequiredScope)
		}
	}
	if compared == 0 {
		t.Fatal("no registered tool route in the generated policy table — this sweep compared nothing")
	}
}

func TestEveryToolRouteDeclaresAGrantableScope(t *testing.T) {
	checked := 0
	for route, pol := range agentPolicies {
		if pol.Access != accessTool {
			continue
		}
		checked++
		if pol.Scope == "" {
			t.Errorf("%s (%s) declares no scope; add an x-mcp-tool scope in api/crm.yaml and regenerate",
				pol.Tool, route)
			continue
		}
		if !grantableScopes[principal.Scope(pol.Scope)] {
			t.Errorf("%s (%s) declares scope %q, which is not in the passport vocabulary — "+
				"no passport could ever be granted it, so the verb is unreachable rather than governed",
				pol.Tool, route, pol.Scope)
		}
	}
	if checked == 0 {
		t.Fatal("no tool route in the generated policy table — this sweep checked nothing")
	}
}
