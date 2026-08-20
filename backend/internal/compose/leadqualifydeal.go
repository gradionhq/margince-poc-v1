// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/people"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// leadDealOpener is the people→deals edge behind qualify-to-deal: the
// promote transaction asks for a deal, and the deals store writes it inside
// that same transaction (CreateDealTx), so the contact and its opportunity
// land together or not at all. Everything runs on the caller's transaction —
// the promote path holds a lead row lock and one pooled connection, and a
// second connection taken while waiting is how a pool deadlocks itself.
type leadDealOpener struct{ deals *deals.Store }

// OpenDealForLead resolves the pipeline and stage — the caller's choice, a
// named stage's own pipeline, or the default pipeline's first open stage —
// and opens the deal.
func (o leadDealOpener) OpenDealForLead(ctx context.Context, tx pgx.Tx, in people.QualifyDealInput) (ids.UUID, error) {
	pipelineID, stageID, err := deals.BirthStageTx(ctx, tx, in.PipelineID, in.StageID)
	if err != nil {
		return ids.Nil, err
	}
	deal, err := o.deals.CreateDealTx(ctx, tx, deals.CreateDealInput{
		Name: in.Name, AmountMinor: in.AmountMinor, Currency: in.Currency,
		PipelineID: pipelineID, StageID: stageID, Source: in.Source,
		// The deal inherits the LEAD's owner exactly — an unassigned lead
		// qualifies into an unassigned deal, not the promoting actor's.
		OwnerID: in.OwnerID, OwnerExact: true,
	})
	if err != nil {
		return ids.Nil, err
	}
	return ids.UUID(deal.Id), nil
}
