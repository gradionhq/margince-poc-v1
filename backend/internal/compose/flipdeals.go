// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Landing an incumbent deal natively. It is the only estate class whose
// arrival takes two steps — born on an open stage, because the store
// forbids any other birth, then advanced to its terminal one — and that
// second step is what makes it the only class the crash repair has to
// finish rather than merely recognize.

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/migration"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func (w *flipWriters) ensureDeal(ctx context.Context, row migration.Row) (migration.EnsureResult, error) {
	owner, disclosure, err := w.resolveOwner(ctx, row, flipObjectDeal)
	if err != nil {
		return migration.EnsureResult{}, err
	}
	stages, err := w.stageCatalog(ctx)
	if err != nil {
		return migration.EnsureResult{}, err
	}
	rawStage := fieldString(row.Fields, "stage_id")
	placement := stages.place(rawStage)

	name := strings.TrimSpace(fieldString(row.Fields, "name"))
	if name == "" {
		name = overlayUnnamed
	}
	in := deals.CreateDealInput{
		Name:       name,
		Currency:   fieldStringPtr(row.Fields, "currency"),
		PipelineID: placement.pipeline,
		StageID:    placement.birthStage,
		OwnerID:    owner,
		Source:     w.provenance(flipObjectDeal, row.ExternalID),
	}
	if minor, ok := fieldInt64(row.Fields, "amount_minor"); ok {
		in.AmountMinor = &minor
	}
	if closeAt, ok := overlayTime(row.Fields, "expected_close_date"); ok {
		in.ExpectedClose = &closeAt
	}
	// The deal is BORN open and advanced afterwards, so its landing and its
	// close are two transactions by the store's own rule. The landing itself
	// is one: the open deal and the identity row that names it commit
	// together, which is what stops a crash here leaving a deal the resume
	// cannot name. The close's own window is settleAdoptedDeal's to finish.
	landed, err := w.landRecord(ctx, flipObjectDeal, row.ExternalID, func(tx pgx.Tx) (ids.UUID, error) {
		deal, err := w.deals.CreateDealTx(ctx, tx, in)
		if err != nil {
			return ids.UUID{}, fmt.Errorf("flip import: creating deal %s: %w", row.ExternalID, err)
		}
		return ids.UUID(deal.Id), nil
	})
	if err != nil {
		return migration.EnsureResult{}, err
	}
	dealID := ids.From[ids.DealKind](landed)

	// A closed estate deal is born open (the store's open-birth-stage
	// rule), then advanced to the terminal stage — the same won/lost path
	// a native close takes, FX freeze included.
	if placement.closedStage != nil {
		var lostReason *string
		if placement.closedSemantic == stageSemanticLost {
			reason := "imported closed-lost from the incumbent estate"
			lostReason = &reason
		}
		if _, err := w.deals.AdvanceDeal(ctx, dealID, deals.AdvanceDealInput{
			ToStageID: *placement.closedStage, LostReason: lostReason,
			// A deal adopted from the incumbent estate has no agreement in
			// Margince, by construction — the paper, if any, lives in the system
			// it came from. `imported` is the true answer, and supplying it is
			// what keeps the gap countable instead of exempting the whole import.
			WonWithoutContractReason: importedWinReason(placement.closedSemantic),
		}); err != nil {
			return migration.EnsureResult{}, fmt.Errorf("flip import: closing imported deal %s: %w", row.ExternalID, err)
		}
	}
	notes := stageDisclosure(placement, rawStage, row.ExternalID)
	if disclosure != "" {
		if notes != "" {
			notes += "; "
		}
		notes += disclosure
	}
	return migration.EnsureResult{Created: true, Disclosure: notes}, nil
}

// settleAdoptedDeal finishes a deal the identity map already binds but
// whose close may never have run.
//
// A closed estate deal reaches its stage in two transactions: born on an
// open stage (the store's open-birth rule), then advanced. The landing
// commits the deal WITH its identity row, so what a crash can leave is a
// deal that is mapped and still open — and without this the import would
// answer Unchanged and leave a closed-won deal parked there, counted as
// converged. It serves an orphan the reconcile adopted from an older
// attempt equally, for the same reason. Re-asserting is idempotent: the
// ordinary replay case finds the deal already terminal and does nothing.
func (w *flipWriters) settleAdoptedDeal(ctx context.Context, dealID ids.DealID, row migration.Row) (migration.EnsureResult, error) {
	stages, err := w.stageCatalog(ctx)
	if err != nil {
		return migration.EnsureResult{}, err
	}
	placement := stages.place(fieldString(row.Fields, "stage_id"))
	if placement.closedStage == nil {
		// The incumbent says this deal is open, which is how it was born.
		return migration.EnsureResult{Unchanged: true}, nil
	}
	deal, err := w.deals.GetDeal(ctx, dealID, storekit.LiveOnly)
	if err != nil {
		return migration.EnsureResult{}, fmt.Errorf("flip import: reading adopted deal %s: %w", row.ExternalID, err)
	}
	if !adoptedDealNeedsClosing(placement, deals.DealStatus(deal.Status)) {
		return migration.EnsureResult{Unchanged: true}, nil
	}
	var lostReason *string
	if placement.closedSemantic == stageSemanticLost {
		reason := "imported closed-lost from the incumbent estate"
		lostReason = &reason
	}
	if _, err := w.deals.AdvanceDeal(ctx, dealID, deals.AdvanceDealInput{
		ToStageID: *placement.closedStage, LostReason: lostReason,
		WonWithoutContractReason: importedWinReason(placement.closedSemantic),
	}); err != nil {
		return migration.EnsureResult{}, fmt.Errorf("flip import: closing adopted deal %s: %w", row.ExternalID, err)
	}
	return migration.EnsureResult{Unchanged: true, Disclosure: fmt.Sprintf(
		"deal %s was recovered from an interrupted attempt and closed on this one", row.ExternalID)}, nil
}

// adoptedDealNeedsClosing is the idempotency rule behind
// settleAdoptedDeal: close only a deal the incumbent says is terminal
// and that is still open natively. Both halves matter — re-advancing an
// already-closed deal would refight a settled close (and its FX freeze),
// while skipping an open one leaves the estate's revenue wrong.
func adoptedDealNeedsClosing(placement flipPlacement, nativeStatus deals.DealStatus) bool {
	return placement.closedStage != nil && nativeStatus == deals.DealOpen
}

// importedWinReason answers the win gate for a deal adopted from an incumbent
// system: `imported` on a win, nothing on a loss. The gate only asks on a win,
// and a reason on a lost deal would be refused by the column's own CHECK.
func importedWinReason(semantic string) *string {
	if semantic == stageSemanticLost {
		return nil
	}
	reason := "imported"
	return &reason
}
