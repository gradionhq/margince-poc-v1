// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The stage-advance lifecycle: won/lost is DERIVED from the target
// stage's semantic, terminal fields (closed_at, lost_reason, frozen FX)
// come and go with the transition, and every move lands in
// deal_stage_history plus the first-class deal.stage_changed event.

package deals

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

type AdvanceDealInput struct {
	ToStageID  ids.StageID
	LostReason *string
	IfVersion  *int64
}

// StagePipelineMismatchError maps to 422: the target stage exists but
// belongs to another pipeline.
type StagePipelineMismatchError struct{ StageID ids.StageID }

func (e *StagePipelineMismatchError) Error() string {
	return "stage " + e.StageID.String() + " does not belong to the deal's pipeline"
}

// LostReasonRequiredError maps to 422 on advancing to a lost stage
// without a reason (deal_lost_reason CHECK, features/01 §3.1).
type LostReasonRequiredError struct{}

func (e *LostReasonRequiredError) Error() string { return "lost_reason is required to close as lost" }

// AdvanceDeal moves a deal one stage, deriving won/lost from the target
// stage's semantic (never from client-supplied status), appending the
// stage history snapshot and emitting the first-class deal.stage_changed
// event — never a generic deal.updated (events.md §1).
func (s *Store) AdvanceDeal(ctx context.Context, id ids.DealID, in AdvanceDealInput) (crmcontracts.Deal, error) {
	if err := auth.Require(ctx, "deal", principal.ActionUpdate); err != nil {
		return crmcontracts.Deal{}, err
	}
	by, err := storekit.CapturedBy(ctx)
	if err != nil {
		return crmcontracts.Deal{}, err
	}

	var out crmcontracts.Deal
	err = s.tx(ctx, func(tx pgx.Tx) error {
		if err := auth.EnsureVisible(ctx, tx, "deal", id.UUID); err != nil {
			return err
		}
		current, err := readDeal(ctx, tx, id, storekit.LiveOnly)
		if err != nil {
			return fmt.Errorf("read deal before advance: %w", err)
		}

		var semantic string
		var stagePipeline ids.PipelineID
		var winProbability int
		err = tx.QueryRow(ctx,
			`SELECT semantic, pipeline_id, win_probability FROM stage WHERE id = $1 AND archived_at IS NULL`,
			in.ToStageID).Scan(&semantic, &stagePipeline, &winProbability)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("resolve target stage: %w", err)
		}
		if stagePipeline.UUID != ids.UUID(current.PipelineId) {
			return &StagePipelineMismatchError{StageID: in.ToStageID}
		}

		p, status, err := stageTransitionPatch(ctx, tx, current, in, semantic)
		if err != nil {
			return err
		}
		if err := p.ApplyGuarded(ctx, tx, "deal", id.UUID, in.IfVersion); err != nil {
			return fmt.Errorf("apply stage advance: %w", err)
		}

		if _, err := tx.Exec(ctx,
			`INSERT INTO deal_stage_history (workspace_id, deal_id, from_stage_id, to_stage_id, changed_by, amount_minor_at_change, currency_at_change)
			 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			storekit.MustWorkspace(ctx), id, ids.UUID(current.StageId), in.ToStageID, by,
			current.AmountMinor, current.Currency); err != nil {
			return fmt.Errorf("record stage history: %w", err)
		}

		auditID, err := storekit.Audit(ctx, tx, "advance_stage", "deal", id.UUID, p.Before(), p.After())
		if err != nil {
			return fmt.Errorf("audit stage advance: %w", err)
		}
		// The §5.3 payload carries the amount snapshot so as-of-date
		// pipeline reports and the overnight stalled/forecast sweep react
		// without a read-back; to_status records the 🟡 won/lost class.
		if err := storekit.Emit(ctx, tx, auditID, "deal.stage_changed", "deal", id.UUID, map[string]any{
			"from_stage_id":          current.StageId,
			"to_stage_id":            in.ToStageID,
			"from_status":            current.Status,
			"to_status":              status,
			"amount_minor_at_change": current.AmountMinor,
			"currency_at_change":     current.Currency,
			"win_probability":        winProbability,
		}); err != nil {
			return fmt.Errorf("emit deal.stage_changed: %w", err)
		}
		if out, err = readDeal(ctx, tx, id, storekit.LiveOnly); err != nil {
			return fmt.Errorf("read advanced deal: %w", err)
		}
		return nil
	})
	return out, err
}

// stageTransitionPatch derives the row changes one stage move implies
// and the resulting status: terminal fields (closed_at, lost_reason,
// frozen FX) are set when the target semantic closes the deal and
// cleared when a won/lost deal reopens.
func stageTransitionPatch(ctx context.Context, tx pgx.Tx, current crmcontracts.Deal, in AdvanceDealInput, semantic string) (*storekit.Patch, string, error) {
	status := "open"
	var closedAt *time.Time
	switch semantic {
	case "won", "lost":
		status = semantic
		now := time.Now().UTC()
		closedAt = &now
		if StageSemantic(semantic) == SemanticLost && (in.LostReason == nil || *in.LostReason == "") {
			return nil, "", &LostReasonRequiredError{}
		}
	}

	p := storekit.NewPatch()
	p.Set("stage_id", current.StageId, in.ToStageID)
	if status != string(current.Status) {
		p.Set("status", current.Status, status)
	}
	if closedAt != nil {
		p.Set("closed_at", current.ClosedAt, *closedAt)
	}
	// lost_reason only exists on a lost deal — never on won or open
	// (on a reopen the terminal-field sweep below clears it; setting
	// it twice would be a malformed UPDATE anyway).
	if DealStatus(status) == DealLost && in.LostReason != nil {
		p.Set("lost_reason", current.LostReason, *in.LostReason)
	}
	// Closing with an amount freezes today's FX rate so base-currency
	// roll-ups stay reproducible (deal_closed_fx).
	if DealStatus(status) != DealOpen && current.AmountMinor != nil && current.Currency != nil {
		rate, rateDate, err := freezeFx(ctx, tx, *current.Currency, time.Now().UTC())
		if err != nil {
			return nil, "", fmt.Errorf("freeze fx at close: %w", err)
		}
		p.Set("fx_rate_to_base", nil, rate)
		p.Set("fx_rate_date", nil, rateDate)
	}
	// Reopening a won/lost deal must clear every terminal field —
	// the DB CHECKs are one-directional, so a stale closed_at or
	// lost_reason on an open deal would silently corrupt forecast
	// and won-lost reporting.
	if DealStatus(status) == DealOpen && DealStatus(current.Status) != DealOpen {
		p.Set("closed_at", current.ClosedAt, nil)
		p.Set("lost_reason", current.LostReason, nil)
		p.Set("fx_rate_to_base", nil, nil)
		p.Set("fx_rate_date", nil, nil)
	}
	return p, status, nil
}

// MissingFxRateError maps to 422: closing a foreign-currency deal needs a
// same-day-or-earlier fx_rate row to freeze.
type MissingFxRateError struct{ From, To string }

func (e *MissingFxRateError) Error() string {
	return "no fx_rate from " + e.From + " to " + e.To + " to freeze at close"
}

// freezeFx resolves the frozen currency→base conversion for a closed
// deal: the latest fx_rate on or before asOf. Used at close (asOf = now)
// and when a closed deal is re-priced (asOf = its close date), so the
// frozen rate always reflects the deal's close, never the edit.
func freezeFx(ctx context.Context, tx pgx.Tx, currency string, asOf time.Time) (string, time.Time, error) {
	asOfDate := asOf.UTC().Truncate(24 * time.Hour)
	var base string
	if err := tx.QueryRow(ctx,
		`SELECT base_currency FROM workspace WHERE id = $1`, storekit.MustWorkspace(ctx)).Scan(&base); err != nil {
		return "", time.Time{}, err
	}
	if currency == base {
		return "1", asOfDate, nil
	}
	var rate string
	err := tx.QueryRow(ctx,
		`SELECT rate::text FROM fx_rate
		 WHERE from_currency = $1 AND to_currency = $2 AND rate_date <= $3
		 ORDER BY rate_date DESC LIMIT 1`,
		currency, base, asOfDate).Scan(&rate)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", time.Time{}, &MissingFxRateError{From: currency, To: base}
	}
	if err != nil {
		return "", time.Time{}, err
	}
	return rate, asOfDate, nil
}
