// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// The generated policy table and the live tool registry declare the same
// tier truth from two sources (contract annotation vs ToolSpec). The
// contract may TIGHTEN (a 🟢-verbed op declared 🟡) but must never sit
// BELOW the tool's own tier — that asymmetry would let the REST twin of a
// 🟡 tool run 🟢. Derived from both live artifacts, not maintained as a
// list.
func TestContractTierNeverBelowRegistryTier(t *testing.T) {
	registry := agents.NewRegistry(stubApprovals{}, nil)
	agents.RegisterCoreTools(registry, nil, nil, nil)

	for route, pol := range agentPolicies {
		if pol.Access != "tool" {
			continue
		}
		spec, registered := registry.Spec(pol.Tool)
		if !registered {
			continue // unregistered verbs default-deny (🟡) or admit at the annotation tier — never below it
		}
		switch spec.Tier {
		case mcp.TierYellow:
			if pol.Tier != "yellow" {
				t.Errorf("%s (%s): tool %s is 🟡 but the contract annotates %q", route, pol.Op, pol.Tool, pol.Tier)
			}
		case mcp.TierDynamic:
			if pol.Tier != "dynamic" && pol.Tier != "yellow" {
				t.Errorf("%s (%s): tool %s is dynamic but the contract annotates %q — the resolver would never run", route, pol.Op, pol.Tool, pol.Tier)
			}
		}
	}
}

// The self-approval class (red-team finding 1) and the config surface the
// advance_deal floor reads (finding 9) must stay human-only in the
// contract: an agent may stage a 🟡 action but never approve one — its
// own least of all — and must not move which stages count as won/lost.
func TestGovernanceOperationsAreHumanOnly(t *testing.T) {
	humanOnly := map[string]bool{
		"approveApproval": true, "rejectApproval": true,
		"recordConsent": true, "createConsentPurpose": true,
		"createDataSubjectRequest": true, "updateDataSubjectRequest": true,
		"createPipeline": true, "updatePipeline": true,
		"createStage": true, "updateStage": true,
		"issuePassport": true, "revokePassport": true,
	}
	seen := map[string]bool{}
	for route, pol := range agentPolicies {
		if humanOnly[pol.Op] {
			seen[pol.Op] = true
			if pol.Access != "human-only" {
				t.Errorf("%s (%s) must be human-only, contract says %q", route, pol.Op, pol.Access)
			}
		}
	}
	for op := range humanOnly {
		if !seen[op] {
			t.Errorf("governance operation %s vanished from the mutating policy table — the human-only pin no longer covers it", op)
		}
	}
}

// operationSpec applies the tighten-only rule: the contract can raise an
// op above its verb's base tier (archive-by-DELETE rides update_record
// but stays 🟡) and a dynamic annotation without a resolvable dynamic
// tool fails closed.
func TestOperationSpecTightenOnly(t *testing.T) {
	registry := agents.NewRegistry(stubApprovals{}, nil)
	agents.RegisterCoreTools(registry, nil, nil, nil)

	spec, ok := operationSpec(agentPolicy{Op: "archivePerson", Access: "tool", Tool: "update_record", Tier: "yellow"}, registry)
	if !ok || spec.Tier != mcp.TierYellow {
		t.Fatalf("🟡 annotation over a 🟢 verb → tier %v ok=%v, want TierYellow (tighten-only)", spec.Tier, ok)
	}

	if _, ok := operationSpec(agentPolicy{Op: "phantom", Access: "tool", Tool: "no_such_tool", Tier: "dynamic"}, registry); ok {
		t.Fatal("dynamic annotation without a registered dynamic tool must fail closed")
	}

	spec, ok = operationSpec(agentPolicy{Op: "sendEmail", Access: "tool", Tool: "send_email", Tier: "yellow"}, registry)
	if !ok || spec.Tier != mcp.TierYellow {
		t.Fatalf("unregistered verb admits at the annotation tier, got %v ok=%v", spec.Tier, ok)
	}
}

// The redemption key is content, not serialization: key order and
// whitespace hash equal; a changed value, path, or operation does not.
func TestCanonicalRESTCallHashesContent(t *testing.T) {
	_, h1, err := canonicalRESTCall("updatePerson", "/v1/people/x", []byte(`{"b":2,"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	_, h2, _ := canonicalRESTCall("updatePerson", "/v1/people/x", []byte(` {"a": 1, "b": 2} `))
	if h1 != h2 {
		t.Fatal("equivalent bodies must hash equal — redemption would refuse the identical call")
	}
	_, h3, _ := canonicalRESTCall("updatePerson", "/v1/people/x", []byte(`{"a":1,"b":3}`))
	_, h4, _ := canonicalRESTCall("updatePerson", "/v1/people/y", []byte(`{"a":1,"b":2}`))
	if h1 == h3 || h1 == h4 {
		t.Fatal("a different body or target must not ride the staged approval")
	}
	if _, _, err := canonicalRESTCall("op", "/p", []byte(`{broken`)); err == nil {
		t.Fatal("malformed JSON must be refused, not hashed")
	}
	_, hEmpty, err := canonicalRESTCall("archivePerson", "/v1/people/x", nil)
	if err != nil || hEmpty == "" {
		t.Fatalf("bodyless mutations (DELETE) must canonicalize: %v", err)
	}
}
