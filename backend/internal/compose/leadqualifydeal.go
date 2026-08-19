// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// leadDealOpener is the people→deals edge behind qualify-to-deal: the
// promote transaction asks for a deal, and the deals store writes it inside
// that same transaction (CreateDealTx), so the contact and its opportunity
// land together or not at all.
type leadDealOpener struct{ deals *deals.Store }

// OpenDealForLead resolves the pipeline and stage — the caller's choice, or
// the default pipeline's first open stage — and opens the deal. A pipeline
// named without a stage takes that pipeline's first open stage.
func (o leadDealOpener) OpenDealForLead(ctx context.Context, tx pgx.Tx, in people.QualifyDealInput) (ids.UUID, error) {
	pipelineID, stageID, err := o.resolveBirthStage(ctx, in.PipelineID, in.StageID)
	if err != nil {
		return ids.Nil, err
	}
	deal, err := o.deals.CreateDealTx(ctx, tx, deals.CreateDealInput{
		Name: in.Name, AmountMinor: in.AmountMinor, Currency: in.Currency,
		PipelineID: pipelineID, StageID: stageID, OwnerID: in.OwnerID, Source: in.Source,
	})
	if err != nil {
		return ids.Nil, err
	}
	return ids.UUID(deal.Id), nil
}

// resolveBirthStage answers (pipeline, stage) for the deal. The reads run on
// the deals store's own transaction — they are catalog reads, and the
// promote transaction holds a lead row lock, never a pipeline lock.
func (o leadDealOpener) resolveBirthStage(ctx context.Context, pipelineID, stageID *ids.UUID) (ids.PipelineID, ids.StageID, error) {
	if pipelineID != nil && stageID != nil {
		return ids.From[ids.PipelineKind](*pipelineID), ids.From[ids.StageKind](*stageID), nil
	}
	var pipeline crmcontracts.Pipeline
	var err error
	if pipelineID != nil {
		pipeline, err = o.deals.GetPipeline(ctx, ids.From[ids.PipelineKind](*pipelineID))
	} else {
		pipeline, err = o.deals.DefaultPipeline(ctx)
	}
	if err != nil {
		return ids.PipelineID{}, ids.StageID{}, fmt.Errorf("resolve the deal's pipeline: %w", err)
	}
	if pipeline.Stages != nil {
		for _, st := range *pipeline.Stages {
			if st.Semantic == crmcontracts.StageSemanticOpen {
				return ids.From[ids.PipelineKind](ids.UUID(pipeline.Id)), ids.From[ids.StageKind](ids.UUID(st.Id)), nil
			}
		}
	}
	return ids.PipelineID{}, ids.StageID{}, fmt.Errorf("pipeline %s has no open stage to open a deal in: %w", pipeline.Name, apperrors.ErrNotFound)
}
