// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The contract does not describe the agent tool surface — it IS the agent tool
// surface. An operation carrying `x-mcp-tool` promises a client that a governed
// tool of that verb exists, and `tools/list` and `GET /v1/agent-tools` publish
// that promise. So a declared verb with no registered tool is not a gap to
// document; it is the contract saying something untrue.
//
// This is the MCP twin of the REST guarantee: `var _ crmcontracts.ServerInterface
// = Server{}` (server.go) makes a declared operation with no handler a compile
// error. Here it is a failing gate rather than a compile error only because a
// registry is populated at runtime, not by an interface.
//
// There is deliberately NO waiver map. A verb that cannot honestly have a tool
// has the wrong annotation, and the fix is `x-agent-access: human-only` in
// api/crm.yaml — which is a statement about authority, not an exemption from
// one. That is why this gate can be absolute where the old pinned backlog could
// not: the escape hatch lives in the contract, where a reviewer sees it.

import (
	"context"
	"encoding/json"
	"sort"
	"testing"

	"github.com/gradionhq/margince/backend/pkg/extension"
)

func TestEveryDeclaredToolVerbIsRegistered(t *testing.T) {
	registry := NewRegistry(nil, SendPath{})

	// route by verb, so the failure names where the reader has to go.
	routes := map[string]string{}
	for route, pol := range agentPolicies {
		if pol.Access != accessTool {
			continue
		}
		if _, registered := registry.Spec(pol.Tool); registered {
			continue
		}
		routes[pol.Tool] = route
	}

	verbs := make([]string, 0, len(routes))
	for verb := range routes {
		verbs = append(verbs, verb)
	}
	sort.Strings(verbs)
	for _, verb := range verbs {
		t.Errorf("%s (%s) declares x-mcp-tool but no tool is registered for it, so the contract "+
			"advertises a verb tools/list cannot offer. Register it, or — if no tool can honestly "+
			"exist for it — give the operation x-agent-access: human-only in api/crm.yaml.",
			verb, routes[verb])
	}
}

// The gate above proves a tool EXISTS for every declared verb. This one proves
// the surface has no tool the contract never declared: an agent could call it,
// and no operation says an agent may.
//
// Both directions, because either alone is satisfied by a surface that is wrong
// in the other. Registry-only tools are legitimate for the §2.2 intents, which
// compose over contract operations rather than backing one, so those are named
// by the verbs the policy table cannot see.
//
// A composed EXTENSION tool is the third legitimate case, and it is declared —
// just not here. Its authority comes from its unit's manifest (ADR-0069), which
// is why an installation can add a verb without editing the contract, and the
// composed set is what a reviewer reads instead of the policy table. Skipping it
// by name rather than by "not in the table" keeps the sweep absolute for
// everything else.
func TestEveryRegisteredToolIsDeclaredOrAnIntent(t *testing.T) {
	declared := map[string]bool{}
	for _, pol := range agentPolicies {
		if pol.Access == accessTool {
			declared[pol.Tool] = true
		}
	}

	specs := NewRegistry(nil, SendPath{}).Specs()
	if len(specs) == 0 {
		t.Fatal("the registry has no tools — this sweep checked nothing")
	}
	composed := composedToolNames()
	for _, spec := range specs {
		if declared[spec.Name] || composedIntents[spec.Name] || composed[spec.Name] {
			continue
		}
		t.Errorf("%s is registered but no operation declares it, so an agent may call a verb the "+
			"contract never granted. Declare the backing operation's x-mcp-tool, or add it to "+
			"composedIntents with the operations it composes over.", spec.Name)
	}
}

// TestTheSweepSkipsExactlyWhatTheComposedSetRegisters: an extension tool reaches
// the registry through the same NewRegistry the sweep above builds, so without
// the composed-set skip an installation with one unit would fail a gate about
// the CONTRACT — and be told to declare an `x-mcp-tool` that would be wrong for
// it. The skip has to cover exactly what registration adds, no more.
func TestTheSweepSkipsExactlyWhatTheComposedSetRegisters(t *testing.T) {
	before := composedToolNames()
	if before["yogi_quote"] {
		t.Fatal("the composed set leaked in from another test — this one would prove nothing")
	}
	tools, err := buildExtensionTools([]extension.Extension{{
		Name: "demo", Version: "1.0.0",
		Tools: []extension.Tool{{
			Name:   "yogi_quote",
			Handle: func(context.Context, extension.Runtime, json.RawMessage) (json.RawMessage, error) { return nil, nil },
		}},
	}}, []extension.Verb{unitVerb("demo", "yogi_quote", extension.TierAutoExecute, extension.ScopeRead)})
	if err != nil {
		t.Fatal(err)
	}
	setComposedTools(tools)
	t.Cleanup(func() { setComposedTools(nil) })

	if !composedToolNames()["yogi_quote"] {
		t.Fatal("a served extension tool is missing from the skip set the sweep consults")
	}
	registered := false
	for _, spec := range NewRegistry(nil, SendPath{}).Specs() {
		if spec.Name == "yogi_quote" {
			registered = true
		}
	}
	if !registered {
		t.Fatal("the composed tool never reached the registry — the skip would be covering nothing")
	}
}

// composedIntents are the §2.2 tools that answer a question by composing several
// contract operations rather than backing one, so no single `x-mcp-tool`
// declares them. Some of them write — `qualify_lead` fills gap-only fields,
// `progress_deal` moves a deal and notes it — which §2.2 sanctions because the
// writes go through the same provider seam the declared CRUD verbs use.
// TestComposedIntentsNeverEgress holds the line that actually matters.
var composedIntents = map[string]bool{
	"catch_me_up_on":           true,
	"prep_for_meeting":         true,
	"who_knows":                true,
	"account_coverage":         true,
	"intro_path_to":            true,
	"at_risk_relationships":    true,
	"whats_slipping_this_week": true,
	"draft_follow_ups_for":     true,
	"list_pipelines":           true,
	"qualify_lead":             true,
	"progress_deal":            true,
	"run_report":               true,
	// query_workspace composes over the same list operations search_records
	// does, but no single one of them can declare it: a plan chooses its target
	// at call time, and the records it selects are read back through the
	// datasource seam. It is read-only and reaches nothing outside the
	// workspace, which is what TestComposedIntentsNeverEgress holds it to.
	"query_workspace": true,
	// search_context ranks across record types through the retrieval index,
	// which no single list operation is: `GET /search` is the lexical half
	// alone and answers no vector lane, and the records the sweep names are
	// read back through the datasource seam. Read-only.
	//
	// It does not EGRESS in the sense this file's rule is about — no record
	// leaves the workspace — but the caller's query string does reach the
	// configured embed provider, exactly as query_workspace's similarity clause
	// does and as every indexed record already did. That is the AI runtime's own
	// lane, governed by the routing config rather than by a passport scope, and
	// the rule below is about outbound authority no operation declared.
	"search_context": true,
	// resolve_entities asks the dedupe ladder a question, which is not an
	// operation at all: `/dedupe/candidates` serves the STORED review queue,
	// a different question from "who does this payload name". Read-only, and
	// every record it names is read back through the datasource seam.
	"resolve_entities": true,
}

// An intent may write inside the workspace; it may NOT reach outside it.
//
// An internal write is bounded by the granting human's own RBAC and row scope,
// which the provider seam applies whatever composed the call. Egress is not: a
// `send` or an `enrich` leaves the workspace, and the operation that would have
// declared it is the only place a reviewer would ever see that. An intent that
// egresses is therefore outbound authority nothing declared — invisible to the
// declaration gate above precisely because no operation declares an intent.
func TestComposedIntentsNeverEgress(t *testing.T) {
	registry := NewRegistry(nil, SendPath{})

	checked := 0
	for name := range composedIntents {
		spec, registered := registry.Spec(name)
		if !registered {
			t.Errorf("%s is listed as a composed intent but is not registered; delete the entry", name)
			continue
		}
		checked++
		if spec.RequiredScope.Egresses() {
			t.Errorf("intent %s spends the outbound %q cap. It backs no contract operation, so "+
				"nothing declares that this surface may leave the workspace — give it a backing "+
				"operation with x-mcp-tool, or keep it inside.", name, spec.RequiredScope)
		}
	}
	if checked == 0 {
		t.Fatal("no composed intents resolved — this sweep asserted nothing")
	}
}
