// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The cap an outbound verb is admitted under is the one the granting human set,
// not a property of the transport. A passport that may not send mail may not
// send a channel message either — one act, two wires.
//
// Outbound is two caps, not one: `send` delivers to a counterparty, `enrich`
// fetches from a third party. Both leave the workspace, so both are held here;
// what must never happen is either of them reachable on `write`.

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

func TestOutboundVerbsRequireAnOutboundCap(t *testing.T) {
	registry := NewRegistry(nil, SendPath{})

	// The outbound universe: every registered tool that declares egress.
	// Deriving it from the registry rather than listing it is what makes a new
	// outbound tool land under this assertion the day it is written.
	leaves := map[string]bool{}
	for _, spec := range registry.Specs() {
		if spec.Egress {
			leaves[spec.Name] = true
		}
	}
	if len(leaves) == 0 {
		t.Fatal("no registered tool declares egress — this sweep asserted nothing")
	}

	checked := 0
	for _, pol := range agentPolicies {
		if pol.Access != accessTool || !leaves[pol.Tool] {
			continue
		}
		checked++
		spec, ok := operationSpec(pol, registry)
		if !ok {
			t.Fatalf("%s: the gate cannot resolve a spec for it", pol.Tool)
		}
		if !spec.RequiredScope.Egresses() {
			t.Errorf("%s leaves the workspace but admits under the %q cap, which does not — a "+
				"passport granted only internal authority reaches outside with it",
				pol.Tool, spec.RequiredScope)
		}
		if spec.Tier != mcp.TierConfirmationRequired {
			t.Errorf("%s admits at tier %v, want TierConfirmationRequired", pol.Tool, spec.Tier)
		}
		if !spec.Egress {
			t.Errorf("%s does not declare egress; it leaves the workspace", pol.Tool)
		}
	}
	// A registered egress tool with no route in the table would leave the loop
	// above asserting nothing while `leaves` was happily non-empty.
	if checked == 0 {
		t.Fatal("no contract route resolves to a registered egress tool — this sweep asserted nothing")
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
