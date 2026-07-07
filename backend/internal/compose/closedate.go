// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Close-date correction wiring (formulas §11, B-E09.20): the deals
// module owns the nightly sweep and the approvals module owns the 🟡
// inbox — this file is the cross-module edge between them, injected
// here like every other one. The sweep stages kind
// "close_date_correction" through the adapter below; a human approval
// releases the confirm effect, which redeems the staging and applies
// the (possibly edited) date through the deals store's own gated
// update path.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/approvals"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/diffhash"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// closeDateStager adapts the approvals service onto the deals module's
// CorrectionStager seam.
type closeDateStager struct {
	svc *approvals.Service
}

func (s closeDateStager) HasPendingCorrection(ctx context.Context, dealID ids.UUID) (bool, error) {
	return s.svc.HasPendingKind(ctx, deals.CloseDateCorrectionKind, dealID)
}

func (s closeDateStager) StageCorrection(ctx context.Context, dealID ids.UUID, targetVersion int64, summary string, proposal deals.CloseDateCorrection) error {
	raw, err := json.Marshal(proposal)
	if err != nil {
		return fmt.Errorf("compose: marshal close-date proposal: %w", err)
	}
	canonical, hash, err := diffhash.Canonical(raw)
	if err != nil {
		return fmt.Errorf("compose: canonicalize close-date proposal: %w", err)
	}
	_, err = s.svc.Stage(ctx, approvals.StageInput{
		Kind:           deals.CloseDateCorrectionKind,
		ProposedChange: canonical,
		DiffHash:       hash,
		TargetType:     "deal",
		TargetID:       dealID,
		TargetVersion:  &targetVersion,
		Summary:        summary,
	})
	return err
}

// NewCloseDateCorrector assembles the nightly close-date corrector for
// the worker process role.
func NewCloseDateCorrector(pool *pgxpool.Pool, log *slog.Logger) *deals.CloseDateCorrector {
	return deals.NewCloseDateCorrector(pool, closeDateStager{svc: approvals.NewService(pool)}, log)
}

// closeDateConfirmEffect executes an approved close-date confirmation:
// redeem-then-execute like every 🟡 executor, then apply the confirmed
// (possibly human-edited) date through the deals store — the same
// RBAC-gated, INV-CLOSE-PAST-validating update a direct edit takes. It
// runs as the deciding human: confirming the date IS their write, and
// their update also clears the provisional flag.
func closeDateConfirmEffect(svc *approvals.Service, store *deals.Store) approvals.ApprovedEffect {
	return func(ctx context.Context, approvalID ids.ApprovalID, proposedChange json.RawMessage, diffHash string) error {
		if err := svc.Redeem(ctx, approvalID, deals.CloseDateCorrectionKind, diffHash); err != nil {
			return err
		}
		correction, err := deals.UnmarshalCloseDateCorrection(proposedChange)
		if err != nil {
			return err
		}
		confirmed, err := time.Parse(time.DateOnly, correction.ExpectedCloseDate)
		if err != nil {
			return fmt.Errorf("compose: confirmed close date: %w", err)
		}
		_, err = store.UpdateDeal(ctx, correction.DealID, deals.UpdateDealInput{ExpectedClose: &confirmed})
		return err
	}
}
