package compose

// Fitness function for the approval surface (M5): every tool the
// registry admits at 🟡 (or dynamically escalates to 🟡) stages
// approvals under its own kind — and the approvals module's decidable()
// fails closed on kinds it has no decision-grant mapping for. A yellow
// tool without a mapping would strand every staging in a queue no inbox
// shows and no human may decide. The tool list is derived from the live
// registry, so registering a new 🟡 tool without extending
// decisionGrants fails here, not in production.

import (
	"context"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// stubApprovals satisfies the registry's staging dependency; the test
// never stages, it only reads the declared surface.
type stubApprovals struct{}

func (stubApprovals) Stage(_ context.Context, _ agents.StageRequest) (ids.UUID, error) {
	return ids.Nil, nil
}
func (stubApprovals) Redeem(_ context.Context, _ ids.UUID, _, _ string) error { return nil }

func TestEveryYellowToolHasADecisionGrantMapping(t *testing.T) {
	registry := agents.NewRegistry(stubApprovals{}, nil)
	agents.RegisterCoreTools(registry, nil, nil, nil)

	for _, spec := range registry.Specs() {
		if spec.Tier == mcp.TierGreen {
			continue // never staged, never decided
		}
		if !approvals.KindHasDecisionGrants(spec.Name) {
			t.Errorf("tool %s can stage approvals (tier %v) but approvals has no decision-grant mapping for it — its stagings would be undecidable", spec.Name, spec.Tier)
		}
	}
}
