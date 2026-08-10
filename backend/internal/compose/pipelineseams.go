// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The seam behind list_pipelines: pipeline configuration for the tool surface,
// read through the deals module's OWN entry point.
//
// Store.ListPipelines already carries auth.Require("pipeline", read) and
// already nests each pipeline's live stages, ordered by position, inside one
// WithWorkspaceTx. So this adapter is a mapping and nothing more — no SQL of
// its own, which is what makes the `pipeline` RBAC object apply to the tool for
// free rather than by a second enforcement that could drift from the first.
//
// Pipelines are CONFIG, not records: they deliberately do not travel the
// datasource record seam, which would put them in the EntityType vocabulary and
// therefore into every polymorphic reference that vocabulary governs.

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/agents"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// pipelineLister answers list_pipelines from the workspace's live pipeline
// configuration.
func pipelineLister(pool *pgxpool.Pool) agents.PipelineLister {
	store := deals.NewStore(pool, InstallationBaseCurrency())
	return func(ctx context.Context) ([]agents.Pipeline, error) {
		rows, err := store.ListPipelines(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]agents.Pipeline, 0, len(rows))
		for _, p := range rows {
			out = append(out, toAgentPipeline(p))
		}
		return out, nil
	}
}

// toAgentPipeline maps one contract pipeline onto the tool shape.
func toAgentPipeline(p crmcontracts.Pipeline) agents.Pipeline {
	out := agents.Pipeline{
		ID:        ids.UUID(p.Id),
		Name:      p.Name,
		IsDefault: p.IsDefault,
		Position:  p.Position,
		Stages:    []agents.Stage{},
	}
	// Stages is an optional member on the contract shape; readPipeline always
	// sets it, so a nil here would mean the read changed underneath us. Either
	// way the tool answers an empty list rather than a null.
	if p.Stages == nil {
		return out
	}
	for _, st := range *p.Stages {
		out.Stages = append(out.Stages, agents.Stage{
			ID:             ids.UUID(st.Id),
			Name:           st.Name,
			Semantic:       string(st.Semantic),
			WinProbability: st.WinProbability,
			Position:       st.Position,
		})
	}
	return out
}
