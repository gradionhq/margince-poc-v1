// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The scope an outbound verb is admitted under is the cap the granting human
// set, not a property of the transport. A passport that may not send mail may
// not send a channel message either — one act, two wires.

import (
	"context"
	"errors"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

func TestOutboundVerbsRequireTheSendScope(t *testing.T) {
	registry := NewRegistry(nil, SendPath{})

	// The outbound universe: every registered tool that declares egress.
	// Deriving it from the registry rather than listing it is what makes a new
	// delivery tool land under this assertion the day it is written.
	delivers := map[string]bool{}
	for _, spec := range registry.Specs() {
		if spec.Egress {
			delivers[spec.Name] = true
		}
	}
	if len(delivers) == 0 {
		t.Fatal("no registered tool declares egress — this sweep asserted nothing")
	}

	checked := 0
	for _, pol := range agentPolicies {
		if pol.Access != accessTool || !delivers[pol.Tool] {
			continue
		}
		checked++
		spec, ok := operationSpec(pol, registry)
		if !ok {
			t.Fatalf("%s: the gate cannot resolve a spec for it", pol.Tool)
		}
		if spec.RequiredScope != principal.ScopeSend {
			t.Errorf("%s admits under scope %q, want %q — a write-only passport can send with it",
				pol.Tool, spec.RequiredScope, principal.ScopeSend)
		}
		if spec.Tier != mcp.TierConfirmationRequired {
			t.Errorf("%s admits at tier %v, want TierConfirmationRequired", pol.Tool, spec.Tier)
		}
		if !spec.Egress {
			t.Errorf("%s does not declare egress; it leaves the workspace", pol.Tool)
		}
	}
	// A registered egress tool with no route in the table would leave the loop
	// above asserting nothing while `delivers` was happily non-empty.
	if checked == 0 {
		t.Fatal("no contract route resolves to a registered egress tool — this sweep asserted nothing")
	}
}

// egressingSynthesizedVerbs states which verbs WITHOUT a registered tool reach
// outside the workspace, and the cap each one spends.
//
// Egress is a fact about the world, not something this code can derive. For a
// synthesized verb the spec's Egress flag is computed FROM its scope, so a scope
// silently weakened to `write` would simply make Egresses() report false and
// agree with itself. Nothing would fail. The fact therefore has to be written
// down once, here, and the contract held to it.
//
// The cap is the one the act's PURPOSE spends, not merely whether packets leave.
// That is why connect_incumbent is `write`: it does call the incumbent, but what
// it durably does is seal a credential and flip the workspace's system of
// record, and scopes are a flat set — `enrich` would admit an enrich-only
// passport to both.
var egressingSynthesizedVerbs = map[string]struct {
	scope  agentScope
	reason string
}{
	"enrich":            {scopeEnrich, "fetches the target's own website through the web-read seam"},
	"reconcile_overlay": {scopeEnrich, "the sweep it queues calls the incumbent's API and spends its budget"},
	"send_offer":        {scopeSend, "the contract promises this send leaves the workspace"},
	"connect_incumbent": {scopeWrite, "calls the incumbent, but its purpose is sealing a credential and flipping x_sor_mode"},
}

// The pin above is the only thing holding an outbound synthesized verb off the
// write cap, so it is worth as much as the gate itself.
func TestEgressingSynthesizedVerbsSpendTheCapTheyArePinnedTo(t *testing.T) {
	registry := NewRegistry(nil, SendPath{})

	seen := map[string]bool{}
	for route, pol := range agentPolicies {
		if pol.Access != accessTool {
			continue
		}
		want, pinned := egressingSynthesizedVerbs[pol.Tool]
		if !pinned {
			continue
		}
		seen[pol.Tool] = true
		if _, registered := registry.Spec(pol.Tool); registered {
			t.Errorf("%s now has a registered tool, so its spec is no longer synthesized — delete its "+
				"egressingSynthesizedVerbs pin and let the registry parity sweep own it", pol.Tool)
			continue
		}
		if pol.Scope != want.scope {
			t.Errorf("%s (%s) is declared scope %q but reaches outside the workspace on the %q cap "+
				"(%s) — either the contract weakened an outbound verb, or the act changed and this pin is stale",
				pol.Tool, route, pol.Scope, want.scope, want.reason)
		}
	}
	for verb := range egressingSynthesizedVerbs {
		if !seen[verb] {
			t.Errorf("%s is pinned as an egressing synthesized verb but no longer appears in the policy "+
				"table; delete the pin", verb)
		}
	}
}

// A spec's Egress flag is what tells an operator the act leaves the
// workspace; its scope is what the passport pays for it. The two are one
// fact, so a spec may not report them differently — `send` and `enrich` both
// put a request on the wire, and a spec claiming either while declaring
// itself workspace-local would be governed correctly and described wrongly.
func TestEverySpecsEgressAgreesWithItsScope(t *testing.T) {
	specs := NewRegistry(nil, SendPath{}).Specs()
	if len(specs) == 0 {
		t.Fatal("the registry has no tools — this sweep checked nothing")
	}
	for _, spec := range specs {
		if want := spec.RequiredScope.Egresses(); spec.Egress != want {
			t.Errorf("%s declares Egress=%v but spends the %q cap, whose egress is %v — "+
				"an operator reading the tool surface would be told the wrong thing about where this act goes",
				spec.Name, spec.Egress, spec.RequiredScope, want)
		}
	}
}

// Resolving a spec is not admitting a call: refusal happens inside
// auth.Gate.Admit, not in the spec resolution above. This is the other half
// of the invariant.
func TestAWriteOnlyPassportIsRefusedTheChannelReply(t *testing.T) {
	pol := agentPolicies["POST /v1/activities/{id}/send-message"]
	spec, ok := operationSpec(pol, NewRegistry(nil, SendPath{}))
	if !ok {
		t.Fatal("the gate cannot resolve a spec for the channel reply")
	}

	ctx := principal.WithWorkspaceID(context.Background(), ids.NewV7())
	ctx = principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:t", OnBehalfOf: ids.NewV7(),
		PassportID: ids.NewV7(),
		Scopes:     principal.NewScopeSet(principal.ScopeRead, principal.ScopeWrite),
	})

	// fullSeat (extensiontools_test.go) is a permissive gate authority — a
	// full seat and empty RBAC — so admission here turns purely on the
	// spec's required scope against the passport's granted scopes, the
	// thing under test. auth.Gate.Admit checks that scope before it ever
	// reads the workspace, so the binding below is not load-bearing for this
	// assertion — it is here only to make the context a realistic agent
	// context.
	if _, err := auth.NewGate(fullSeat{}).Admit(ctx, spec, nil); !errors.Is(err, apperrors.ErrScopeExceeded) {
		t.Errorf("a write-only passport was admitted to the channel reply: err = %v, want ErrScopeExceeded", err)
	}
}
