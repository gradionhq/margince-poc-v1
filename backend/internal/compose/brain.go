// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The model path, assembled over the ai module's tiered router so every
// model consumer — the Surface-B runner, the retrieval embed lane, the
// cold-start read-back — rides routing policy, the budget guardrail,
// metering and secret-stripping through ONE router. Consumers only ever
// see the narrow Brain seam.

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/agents/runner"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/search"
	"github.com/gradionhq/margince/backend/internal/shared/ports/model"
)

// ModelPath is the wired model surface one process role hands around:
// each lane is the same router under a task label, so ai-routing.yaml
// decides the tier per workload, and every lane draws on the
// seat-derived monthly budget.
type ModelPath struct {
	Agent     runner.Brain    // the Surface-B reason-act loop
	ColdStart runner.Brain    // the website read-back extraction
	BriefRank runner.Brain    // the Morning-Brief L2 re-order (B-E05.2)
	Embedder  search.Embedder // the retrieval embed lane
}

// NewModelPath builds the production model path from a validated
// routing config.
func NewModelPath(cfg ai.RoutingConfig, pool *pgxpool.Pool) (ModelPath, error) {
	router, err := ai.NewRouter(cfg, ai.NewMeter(pool), NewSeatBudget(pool))
	if err != nil {
		return ModelPath{}, err
	}
	return ModelPath{
		Agent:     routerBrain{router: router, task: ai.TaskAgentLoop},
		ColdStart: routerBrain{router: router, task: ai.TaskColdStart},
		BriefRank: routerBrain{router: router, task: ai.TaskBriefRanking},
		Embedder:  router,
	}, nil
}

// FakeModelPath drives every lane with one offline fake — the dev/test
// path behind an explicit flag, never a silent default.
func FakeModelPath(client *ai.FakeClient) ModelPath {
	return ModelPath{Agent: client, ColdStart: client, BriefRank: client, Embedder: client}
}

type routerBrain struct {
	router *ai.Router
	task   ai.Task
}

func (b routerBrain) Complete(ctx context.Context, req model.Request) (model.Response, error) {
	resp, _, err := b.router.Complete(ctx, b.task, req)
	return resp, err
}

// CompleteValidated exposes the §5.2 structured-output pipeline
// (validate → retry with feedback → escalate a tier) on the lane's own
// task label.
func (b routerBrain) CompleteValidated(ctx context.Context, req model.Request, validate ai.Validator) (model.Response, error) {
	resp, _, err := b.router.CompleteStructured(ctx, b.task, req, validate)
	return resp, err
}
