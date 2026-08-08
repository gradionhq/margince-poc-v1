// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Fitness function for the approval surface (M5): every tool the
// registry admits at 🟡 (or dynamically escalates to 🟡) stages
// approvals under its own kind — and the approvals module's decidable()
// fails closed on kinds it has no decision-grant mapping for. A
// confirmation_required tool without a mapping would strand every staging in a queue no inbox
// shows and no human may decide. The tool list is derived from the live
// registry, so registering a new 🟡 tool without extending
// decisionGrants fails here, not in production.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
	"github.com/gradionhq/margince/backend/internal/shared/ports/retrieval"
)

// stubApprovals satisfies the registry's staging dependency; the test
// never stages, it only reads the declared surface.
type stubApprovals struct{}

// StageQuotaRelease satisfies the seam; a step-up never reaches these tests.
func (stubApprovals) StageQuotaRelease(_ context.Context, _ agents.QuotaReleaseRequest) (ids.ApprovalID, bool, error) {
	return ids.ApprovalID{}, false, nil
}

func (stubApprovals) Stage(_ context.Context, _ agents.StageRequest) (ids.ApprovalID, error) {
	return ids.ApprovalID{}, nil
}

func (stubApprovals) Redeem(_ context.Context, _ ids.ApprovalID, _, _ string) (int64, bool, error) {
	return 0, false, nil
}

// stubRetriever/stubComms exist so the derived tool list covers the
// intent and comms registrations; the test only reads Specs().
type stubRetriever struct{}

func (stubRetriever) Search(context.Context, retrieval.Query) (retrieval.Result, error) {
	return retrieval.Result{}, nil
}

func (stubRetriever) AssembleContext(context.Context, datasource.EntityRef, retrieval.AssembleOptions) (retrieval.Context, error) {
	return retrieval.Context{}, nil
}

type stubComms struct{}

func (stubComms) DraftEmail(context.Context, ids.UUID, string) (string, string, error) {
	return "", "", nil
}

func (stubComms) SendEmail(context.Context, ids.UUID, agents.SendEmailArgs) (agents.SendEmailResult, error) {
	return agents.SendEmailResult{}, nil
}

func (stubComms) SendMessage(context.Context, ids.UUID, agents.SendMessageArgs) (agents.SendMessageResult, error) {
	return agents.SendMessageResult{}, nil
}

func (stubComms) IsChannelKind(kind string) bool { return activities.IsChannelKind(kind) }

func (stubComms) Availability(context.Context, *ids.UUID, time.Time, time.Time, int) (agents.AvailabilityResult, error) {
	return agents.AvailabilityResult{}, nil
}

func (stubComms) BookMeeting(context.Context, agents.BookMeetingArgs) (json.RawMessage, error) {
	return nil, nil
}

// stubSoR satisfies the record reader the 🟡 comms verbs stage against. This
// walk only reads Specs(), so nothing calls it — but RegisterCommsTools now
// refuses a nil provider at wiring time, because a surface that advertises
// three sends and panics on them is worse than one that will not boot.
type stubSoR struct {
	datasource.SystemOfRecordProvider
}

func TestEveryConfirmationRequiredToolHasADecisionGrantMapping(t *testing.T) {
	registry := agents.NewRegistry(stubApprovals{}, nil)
	agents.RegisterCoreTools(registry, nil, nil, nil, nil)
	agents.RegisterIntentTools(registry, stubRetriever{})
	agents.RegisterCommsTools(registry, stubComms{}, stubSoR{})

	for _, spec := range registry.Specs() {
		if spec.Tier == mcp.TierAutoExecute {
			continue // never staged, never decided
		}
		if !approvals.KindHasDecisionGrants(spec.Name) {
			t.Errorf("tool %s can stage approvals (tier %v) but approvals has no decision-grant mapping for it — its stagings would be undecidable", spec.Name, spec.Tier)
		}
	}
}

// Every kind with a REGISTERED EFFECT must also be decidable. The tool-registry
// sweep above misses these: a compose-registered effect kind is stageable
// without being an agent tool, and requireDecisionGrants fails closed on an
// unmapped kind — so the proposals would be staged, hidden from every list, and
// undecidable, while the ledger row that pointed at them waited forever.
//
// This is the obligation stated as "every stageable kind", which is what the
// system actually guarantees, rather than "every tool", which is where the
// first version of this test happened to look.
func TestEveryRegisteredEffectKindHasADecisionGrantMapping(t *testing.T) {
	svc := approvalsServiceWithEffects(nil)
	kinds := svc.EffectKinds()
	if len(kinds) == 0 {
		t.Fatal("no effect kinds registered — the scan found nothing to check, which means it is broken")
	}
	for _, kind := range kinds {
		if !approvals.KindHasDecisionGrants(kind) {
			t.Errorf("kind %q has a registered approved-effect but no decision-grant mapping — its proposals would be staged and then be undecidable by anyone", kind)
		}
	}
}

// The kind string is not a namespace, and the two writers of it do not
// coordinate: the REST admission gate stages under the operation's TOOL
// name, while compose registers approved-effect executors under kinds its
// own proposal flows mint. "enrich" is both — the scrape proposal's kind
// and the tool behind coldStartReadback, deepReadCompany and scrapeCompany
// — so an agent could mint a staging that a human's approve click would
// feed to the compose enrichment executor.
//
// This test names the overlap rather than forbidding it, because forbidding
// it would mean either renaming stored kinds or refusing three legitimate
// confirm-first agent routes. What the system guarantees instead is that
// provenance decides: an executor runs only for a staging with no passport.
// The list below is the evidence that the guarantee is load-bearing — if it
// ever empties, the collision is gone and this test should go with it.
func TestCollidingEffectKindsAreCoveredByProvenance(t *testing.T) {
	svc := approvalsServiceWithEffects(nil)
	effects := map[string]bool{}
	for _, kind := range svc.EffectKinds() {
		effects[kind] = true
	}
	if len(effects) == 0 {
		t.Fatal("no effect kinds registered — the scan found nothing to check, which means it is broken")
	}

	colliding := map[string]string{}
	for route, pol := range agentPolicies {
		if pol.Access == accessTool && effects[pol.Tool] {
			colliding[pol.Tool] = route
		}
	}
	if len(colliding) == 0 {
		t.Error("no agent tool name collides with a registered effect kind — the provenance check in " +
			"approvals.decide now guards nothing, so delete it and delete this test rather than " +
			"leaving a control nobody can see is dead")
	}
	for tool, route := range colliding {
		t.Logf("agent route %s stages kind %q, which also has a server-side effect executor — "+
			"only the no-passport check keeps a human's approve click from running it", route, tool)
	}
}

// Every confirm-first row the CONTRACT declares has a decision mapping too —
// including a verb no tool implements.
//
// The registry sweep above walks what is registered, which is why it could not
// see #484: `connect_incumbent` was declared confirmation_required by an
// operation with no registered tool at all, so an agent's call cleared the
// admission gate, reached stageRefusal, found no mapping and answered 403. It
// was fail-closed and it was also unreachable capability the contract kept
// advertising — a shape only the generated policy table shows, since that table
// is the contract's own reading of itself.
//
// There is deliberately NO waiver. A verb that cannot honestly be staged has
// the wrong annotation, and the fix is `x-agent-access: human-only` in
// api/crm.yaml — a statement about authority, made where a reviewer sees it,
// rather than an exemption from one. That is exactly how #484's own operations
// were resolved.
func TestEveryConfirmationRequiredPolicyHasAnApprovalKind(t *testing.T) {
	checked := 0
	for route, pol := range agentPolicies {
		if pol.Access != accessTool || pol.Tier == tierAutoExecute {
			continue
		}
		checked++
		if approvals.KindHasDecisionGrants(pol.Tool) {
			continue
		}
		t.Errorf("%s declares %s at %v, and approvals has no decision-grant mapping for that kind. "+
			"Every call to it clears admission and is then refused at staging, so the contract "+
			"advertises a governed verb no agent can ever reach. Map the kind in "+
			"approvals.decisionGrants, or annotate the operation x-agent-access: human-only.",
			route, pol.Tool, pol.Tier)
	}
	if checked == 0 {
		t.Fatal("no confirm-first tool routes in the generated policy — the gate covers nothing")
	}
}
