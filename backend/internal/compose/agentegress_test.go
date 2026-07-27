// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The egress obligation as a fitness function: an operation that lets its
// CALLER name a URL the server will fetch must never be 🟢 for an agent.
//
// A caller-chosen absolute URL, fetched from the server's own address with
// the caller's path and query, is an outbound channel whose destination the
// product does not choose. netguard refuses non-public addresses, which is
// the SSRF question; it is not this one — an attacker's own internet host is
// public by definition, and the request reaches it before any model call, so
// the fetch succeeds even when the extraction it was ostensibly for fails.
// The only control that binds is a human deciding the call, which is what 🟡
// means.
//
// Derived from the contract, not maintained as a list: the URL-taking set is
// whatever the request schemas say it is, so a new endpoint that accepts one
// is covered the day it is written.

import (
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// urlTakingOperations answers the operationIds whose request body declares a
// caller-supplied absolute URL. Schemas are resolved through their $refs, so
// an operation reusing a shared request shape is found the same way one
// declaring it inline is.
func urlTakingOperations(t *testing.T, doc *openapi3.T) map[string]string {
	t.Helper()
	ops := map[string]string{}
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			if op.RequestBody == nil || op.RequestBody.Value == nil {
				continue
			}
			for _, media := range op.RequestBody.Value.Content {
				if media.Schema == nil || media.Schema.Value == nil {
					continue
				}
				prop, declared := media.Schema.Value.Properties["url"]
				if !declared || prop.Value == nil || prop.Value.Format != "uri" {
					continue
				}
				ops[op.OperationID] = method + " /v1" + path
			}
		}
	}
	return ops
}

func TestUrlTakingOperationsAreNeverAutoExecuteForAgents(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromFile("../../api/crm.yaml")
	if err != nil {
		t.Fatalf("loading the contract: %v", err)
	}

	taking := urlTakingOperations(t, doc)
	if len(taking) == 0 {
		t.Fatal("no URL-taking operations found in the contract — the gate would pass vacuously")
	}

	byOp := map[string]agentPolicy{}
	for _, pol := range agentPolicies {
		byOp[pol.Op] = pol
	}

	checked := 0
	for op, route := range taking {
		pol, governed := byOp[op]
		if !governed {
			// A route with no policy entry is refused outright for agents
			// (the gate is fail-closed on an unknown mutating route), so it
			// cannot reach the fetch.
			continue
		}
		checked++
		if pol.Access != accessTool {
			continue // human-only: an agent principal is rejected before the handler
		}
		if pol.Tier == tierAutoExecute {
			t.Errorf("%s (%s) lets an agent name the URL the server fetches and is annotated auto_execute — "+
				"an unapproved outbound request to a destination the product did not choose", route, op)
		}
	}
	if checked == 0 {
		t.Fatal("no URL-taking operation is in the agent policy table — the pin no longer covers anything")
	}
}

// The synthesized ToolSpec for a verb with no registered tool is the path
// coldStartPreview escaped through: `read` is not a registered tool, so the
// gate built a spec from the annotation alone. That fallback is legitimate —
// most contract verbs have no tool twin — but it must carry the annotated
// tier faithfully rather than defaulting to 🟢, because the annotation is
// then the ONLY thing standing between an agent and the effect.
func TestUnregisteredVerbCarriesTheAnnotatedTier(t *testing.T) {
	registry := agents.NewRegistry(stubApprovals{}, nil)

	spec, ok := operationSpec(agentPolicy{
		Op: "coldStartPreview", Access: accessTool, Tool: "enrich", Tier: tierConfirmationRequired,
	}, registry)
	if !ok {
		t.Fatal("an unregistered verb at a static tier must resolve, not fail closed")
	}
	if spec.Tier != mcp.TierConfirmationRequired {
		t.Errorf("unregistered verb annotated confirmation_required resolved to tier %v — "+
			"the annotation is the only gate an unregistered verb has", spec.Tier)
	}
}

// coldStartPreview performs the same outbound fetch as its sibling
// POST /coldstart, from the same code path, before any model call. The two
// were annotated differently — the write effect was tiered, the egress
// effect was not — so this pins them together.
func TestColdStartPreviewMatchesItsSiblingsTier(t *testing.T) {
	preview, ok := agentPolicies["POST /v1/coldstart/preview"]
	if !ok {
		t.Fatal("POST /v1/coldstart/preview left the policy table")
	}
	readback, ok := agentPolicies["POST /v1/coldstart"]
	if !ok {
		t.Fatal("POST /v1/coldstart left the policy table")
	}
	if preview.Tier != readback.Tier {
		t.Errorf("coldStartPreview is %q while coldStartReadback is %q — they issue the SAME outbound fetch",
			preview.Tier, readback.Tier)
	}
	if preview.Tier == tierAutoExecute {
		t.Errorf("coldStartPreview is %q: an agent-chosen URL is fetched with no human in the loop", preview.Tier)
	}
}

// `x-agent-access: human-only` binds a read exactly as it binds a write,
// and the generated table is the only place the gate can learn it. Emitting
// mutating rows alone is what previously turned the contract's most
// explicit agent prohibition into a comment: 37 human-only GET operations
// existed and ZERO GET keys did, so the refusal was structurally
// unreachable for every one of them.
//
// Derived from the contract in both directions — every annotated read has a
// row, and every row says what the contract says — so a new human-only read
// is covered the day it is annotated.
func TestEveryHumanOnlyReadReachesTheGate(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromFile("../../api/crm.yaml")
	if err != nil {
		t.Fatalf("loading the contract: %v", err)
	}

	reads := 0
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			if mutatingMethod(method) {
				continue
			}
			access, annotated := op.Extensions["x-agent-access"].(string)
			if !annotated {
				continue // ordinary agent-readable data; the gate admits it
			}
			reads++
			route := method + " /v1" + path
			pol, known := agentPolicies[route]
			if !known {
				t.Errorf("%s (%s) is annotated x-agent-access: %s but has no policy row — "+
					"the gate cannot refuse what the generator dropped", route, op.OperationID, access)
				continue
			}
			if string(pol.Access) != access {
				t.Errorf("%s (%s): contract says %q, generated table says %q", route, op.OperationID, access, pol.Access)
			}
		}
	}
	if reads == 0 {
		t.Fatal("no human-only reads found in the contract — the gate would pass vacuously")
	}
}
