// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Specs for the egress backstop under every write into an external system of
// record. It lives at the seam because the tool is not the only route: the
// REST twin of update_record and qualify_lead both reach the incumbent
// through the same dispatch, so a per-tool gate could never be the control.
// A human in their own seat is object RBAC's question, not this gate's
// (ADR-0055's split).

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/overlay"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// egressAgentCtx is a passport-shaped principal: the type every agent
// surface authenticates as, and the only one this gate governs.
func egressAgentCtx() context.Context {
	return principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:t", OnBehalfOf: ids.NewV7(),
		PassportID: ids.NewV7(),
		Scopes:     principal.NewScopeSet(principal.ScopeWrite),
	})
}

// The refusal is the DECLARED unsupported-by-SoR answer, not "needs
// approval": there is no approval an agent could get for this in this build,
// and naming a path that dead-ends is what the sentinel exists to avoid.
func TestEgressBackstopRefusesAnUnreleasedAgentWrite(t *testing.T) {
	err := refuseUngovernedAgentEgress(egressAgentCtx(), overlay.WriteUpdate, datasource.EntityPerson)

	if !errors.Is(err, apperrors.ErrUnsupportedBySoR) {
		t.Fatalf("err = %v, want ErrUnsupportedBySoR", err)
	}
}

// A verb the overlay adapter never serves is the provider's answer to give,
// with its own object-RBAC and row-scope checks — not this gate's. Otherwise
// an agent is told to seek approval for something permanently unsupported.
func TestEgressBackstopLeavesUnsupportedVerbsToTheProvider(t *testing.T) {
	if err := refuseUngovernedAgentEgress(egressAgentCtx(), overlay.WriteCreate, datasource.EntityPerson); err != nil {
		t.Errorf("Create err = %v, want nil (the provider declares it unsupported)", err)
	}
	if err := refuseUngovernedAgentEgress(egressAgentCtx(), overlay.WriteArchive, datasource.EntityLead); err != nil {
		t.Errorf("Archive of a non-archivable type err = %v, want nil", err)
	}
}

// The REST agent gate is the second dispatch layer, and it must mark what it
// redeems: the seam backstop reads that marker, so a gate that redeems an
// approval without setting it would refuse the very write the human just
// authorized — the approval would be granted for a call that can never run.
// The MCP registry's own marking is covered by its redemption tests; this
// pins the REST half, which had no such coverage.
func TestRESTRedemptionMarksTheCallReleasedForTheSeam(t *testing.T) {
	var seen bool
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = agents.ApprovalRedeemed(r.Context())
	})

	approvalID := ids.New[ids.ApprovalKind]()
	req := httptest.NewRequest(http.MethodPatch, "/v1/people/"+ids.NewV7().String(), http.NoBody)
	req.Header.Set(approvalTokenHeader, approvalID.String())

	handled := redeemIfPresented(httptest.NewRecorder(), req, next, stubApprovals{},
		agentPolicy{Op: "updatePerson", Access: "tool", Tool: "update_record", RecordType: "person"}, []byte(`{}`))

	if !handled {
		t.Fatal("a presented approval token was not consumed by the REST gate")
	}
	if !seen {
		t.Error("the redeemed call reached the handler unmarked — the seam backstop would refuse an approved write")
	}
}

// The mirror image: a request with no token must NOT look released, or the
// backstop would wave every unapproved agent write through.
func TestRESTGateDoesNotMarkAnUnredeemedCall(t *testing.T) {
	var seen bool
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = agents.ApprovalRedeemed(r.Context())
	})
	req := httptest.NewRequest(http.MethodPatch, "/v1/people/"+ids.NewV7().String(), http.NoBody)

	if redeemIfPresented(httptest.NewRecorder(), req, next, stubApprovals{}, agentPolicy{Tool: "update_record"}, []byte(`{}`)) {
		t.Fatal("a request with no approval token was treated as a redemption")
	}
	if seen {
		t.Error("an unredeemed call was marked released")
	}
}

// The one branch that OPENS the gate. Its failure mode is an ungoverned write
// landing in a third party's CRM, so it is pinned rather than left to the
// branches that close it. The marker is set only by a dispatch layer straight
// after its own Redeem, so it stands for "a human released exactly this call".
func TestEgressBackstopAllowsAReleasedAgentWrite(t *testing.T) {
	// Obtained the only way it can be: by actually redeeming. That the marker
	// cannot be minted any other way is the point — a doc comment asking callers
	// not to forge it was not enforcement.
	ctx, _, _, err := agents.RedeemAndMark(egressAgentCtx(), stubApprovals{},
		ids.New[ids.ApprovalKind](), "update_record", "hash")
	if err != nil {
		t.Fatalf("redeeming: %v", err)
	}

	if err := refuseUngovernedAgentEgress(ctx, overlay.WriteUpdate, datasource.EntityPerson); err != nil {
		t.Fatalf("err = %v, want nil for a released call", err)
	}
}

// A person acting in their own seat is governed by object RBAC; gating them
// here would break the human write path the SPA and REST both offer.
func TestEgressBackstopDoesNotGateAHumanSeat(t *testing.T) {
	if err := refuseUngovernedAgentEgress(humanCtx(), overlay.WriteUpdate, datasource.EntityPerson); err != nil {
		t.Fatalf("err = %v, want nil for a human seat", err)
	}
}

// A system principal is not an agent acting on a prompt: background sweeps
// carry it, and so does automation's action executor, whose overlay write-back
// compose/workflows.go documents as intended. The allowance is deliberate —
// standing automation is authored by a human and governed at author time.
func TestEgressBackstopDoesNotGateASystemOrUnboundContext(t *testing.T) {
	system := principal.WithActor(context.Background(), principal.Principal{
		Type: principal.PrincipalSystem, ID: "system:reconcile",
	})
	if err := refuseUngovernedAgentEgress(system, overlay.WriteUpdate, datasource.EntityPerson); err != nil {
		t.Errorf("system principal err = %v, want nil", err)
	}
	if err := refuseUngovernedAgentEgress(context.Background(), overlay.WriteUpdate, datasource.EntityPerson); err != nil {
		t.Errorf("unbound context err = %v, want nil", err)
	}
}

// Rule 2 — derive the obligation, don't maintain it as a list. The entity axis
// is already by construction (the gate calls the same SupportsWrite the
// provider does), but the VERB axis is not: if SupportsWrite ever answers true
// for a verb whose Dispatcher method has no backstop, an agent write reaches
// the incumbent ungoverned. This fails the day that happens rather than
// leaving it to a reviewer to notice.
func TestEveryOverlayServedWriteVerbIsBackstopped(t *testing.T) {
	// The verbs the gate is wired under, by the Dispatcher method that calls it.
	backstopped := map[overlay.WriteVerb]bool{
		overlay.WriteUpdate:  true,
		overlay.WriteArchive: true,
	}
	// Both axes derived: the verbs from the overlay module, the mirrored types
	// from the same map the REST write guard keys on. A newly mirrored type or a
	// fourth verb is then covered by this claim without anyone remembering to
	// extend a list here.
	for _, verb := range overlay.AllWriteVerbs() {
		for recordType := range overlayMirroredTypes {
			et := datasource.EntityType(recordType)
			if !overlay.SupportsWrite(verb, et) || backstopped[verb] {
				continue
			}
			t.Errorf("overlay serves %v on %s but Dispatcher has no egress backstop for that verb — "+
				"an agent write would reach the incumbent ungoverned", verb, et)
		}
	}
}
