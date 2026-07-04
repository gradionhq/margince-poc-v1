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
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// NewModelPath builds the production model path from a validated
// routing config: the runner's brain and the retrieval embed lane, both
// through the same router (budget, metering, stripping). The
// seat-count-derived budget lands here when compose wires it; until
// then the single-seat default applies.
func NewModelPath(cfg ai.RoutingConfig, pool *pgxpool.Pool) (runner.Brain, search.Embedder, error) {
	router, err := ai.NewRouter(cfg, ai.NewMeter(pool), ai.DefaultMonthlyTokens)
	if err != nil {
		return nil, nil, err
	}
	return routerBrain{router: router}, router, nil
}

type routerBrain struct{ router *ai.Router }

func (b routerBrain) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	resp, _, err := b.router.Complete(ctx, ai.TaskAgentLoop, req)
	return resp, err
}
