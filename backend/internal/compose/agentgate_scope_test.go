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

	outbound := map[string]string{
		"send_email":   "POST /v1/activities/{id}/send-email",
		"send_message": "POST /v1/activities/{id}/send-message",
	}

	for verb, route := range outbound {
		pol, known := agentPolicies[route]
		if !known {
			t.Fatalf("%s: route %q is absent from the generated policy table", verb, route)
		}
		if pol.Tool != verb {
			t.Fatalf("%s: route %q declares tool %q", verb, route, pol.Tool)
		}
		spec, ok := operationSpec(pol, registry)
		if !ok {
			t.Fatalf("%s: the gate cannot resolve a spec for it", verb)
		}
		if spec.RequiredScope != principal.ScopeSend {
			t.Errorf("%s admits under scope %q, want %q — a write-only passport can send with it",
				verb, spec.RequiredScope, principal.ScopeSend)
		}
		if spec.Tier != mcp.TierConfirmationRequired {
			t.Errorf("%s admits at tier %v, want TierConfirmationRequired", verb, spec.Tier)
		}
		if !spec.Egress {
			t.Errorf("%s does not declare egress; it leaves the workspace", verb)
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
