package compose

// The runner's brain, assembled over the ai module's tiered router so a
// Surface-B run rides routing policy, the budget guardrail, metering
// and secret-stripping like every other model consumer. agents/runner
// only ever sees the narrow Brain seam.

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/agents/runner"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// NewRouterBrain builds the production brain from a validated routing
// config. The seat-count-derived budget lands here when compose wires
// it; until then the single-seat default applies.
func NewRouterBrain(cfg ai.RoutingConfig, pool *pgxpool.Pool) (runner.Brain, error) {
	router, err := ai.NewRouter(cfg, ai.NewMeter(pool), ai.DefaultMonthlyTokens)
	if err != nil {
		return nil, err
	}
	return routerBrain{router: router}, nil
}

type routerBrain struct{ router *ai.Router }

func (b routerBrain) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	resp, _, err := b.router.Complete(ctx, ai.TaskAgentLoop, req)
	return resp, err
}
