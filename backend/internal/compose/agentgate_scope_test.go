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

	// The outbound universe: every verb still pinned as a hole, plus every
	// registered tool that declares egress. Deriving it is what makes closing a
	// hole at the wrong scope fail here rather than pass quietly.
	outbound := map[string]bool{}
	for verb := range outboundHoles {
		outbound[verb] = true
	}
	for _, spec := range NewRegistry(nil, SendPath{}).Specs() {
		if spec.Egress {
			outbound[spec.Name] = true
		}
	}

	for route, pol := range agentPolicies {
		if pol.Access != accessTool || !outbound[pol.Tool] {
			continue
		}
		if _, registered := registry.Spec(pol.Tool); !registered {
			// A pinned hole is known-wrong — outboundHoles names the debt,
			// not a passing grade — so it must not be asserted against
			// ScopeSend here. TestThePinsDescribeVerbsThatStillExist covers
			// the other half: it fails the moment the verb gains a tool,
			// which is when this branch starts asserting on it instead.
			if _, pinned := outboundHoles[pol.Tool]; !pinned {
				t.Errorf("%s (%s) is derived as outbound but is neither registered nor a pinned hole", pol.Tool, route)
			}
			continue
		}
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
}

// Resolving a spec is not admitting a call: refusal happens in Admit
// (agentgate.go:129). This is the other half of the invariant.
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
	// thing under test. The workspace binding is what lets Admit reach that
	// scope decision instead of failing closed on a missing tenant first.
	if _, err := auth.NewGate(fullSeat{}).Admit(ctx, spec, nil); !errors.Is(err, apperrors.ErrScopeExceeded) {
		t.Errorf("a write-only passport was admitted to the channel reply: err = %v, want ErrScopeExceeded", err)
	}
}
