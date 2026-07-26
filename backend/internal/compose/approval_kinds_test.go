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

func (stubApprovals) Stage(_ context.Context, _ agents.StageRequest) (ids.ApprovalID, error) {
	return ids.ApprovalID{}, nil
}
func (stubApprovals) Redeem(_ context.Context, _ ids.ApprovalID, _, _ string) error { return nil }

// stubRetriever/stubComms exist so the derived tool list covers the
// intent and comms registrations; the test only reads Specs().
type stubRetriever struct{}

func (stubRetriever) Search(context.Context, retrieval.Query) ([]retrieval.Hit, error) {
	return nil, nil
}
func (stubRetriever) AssembleContext(context.Context, datasource.EntityRef, retrieval.AssembleOptions) (retrieval.Context, error) {
	return retrieval.Context{}, nil
}

type stubComms struct{}

func (stubComms) DraftEmail(context.Context, ids.UUID, string) (string, string, error) {
	return "", "", nil
}
func (stubComms) SendEmail(context.Context, ids.UUID, agents.SendEmailArgs) (json.RawMessage, error) {
	return nil, nil
}
func (stubComms) Availability(context.Context, *ids.UUID, time.Time, time.Time, int) (json.RawMessage, error) {
	return nil, nil
}
func (stubComms) BookMeeting(context.Context, agents.BookMeetingArgs) (json.RawMessage, error) {
	return nil, nil
}

func TestEveryConfirmationRequiredToolHasADecisionGrantMapping(t *testing.T) {
	registry := agents.NewRegistry(stubApprovals{}, nil)
	agents.RegisterCoreTools(registry, nil, nil, nil, nil)
	agents.RegisterIntentTools(registry, stubRetriever{})
	agents.RegisterCommsTools(registry, stubComms{})

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
