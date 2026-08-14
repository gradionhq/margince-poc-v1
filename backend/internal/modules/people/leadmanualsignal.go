// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// Manual scoring input (S-E13.6, ADR-0105/A156): what a rep knows and
// capture cannot fetch — a traffic band, an employee count, a budget hint.
//
// It feeds the same transparent weighted score as an auto-captured signal
// and appears in the decomposition as its own human-provided factor, never
// blended into a machine one. The written reason is mandatory: a scoring
// input nobody can account for is the thing this feature exists to end.

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

// leadManualFactorBands is the §24 catalog: the factors a human may supply
// and the bands each accepts, with the points a band contributes. Closed by
// construction — a factor outside it is a 422, not a new column.
var leadManualFactorBands = map[string]map[string]int{
	"web_traffic": {"low": 0, "medium": 4, "high": 8},
	"employees":   {"1-10": 0, "11-50": 4, "51-200": 8, "201+": 10},
	"budget_hint": {"none": -4, "unknown": 0, "some": 4, "confirmed": 10},
}

// SetLeadManualSignalInput is one human-supplied factor.
type SetLeadManualSignalInput struct {
	Factor     string
	Band       string
	SignalKind string
	Confidence *float32
	Reason     string
}

// SetLeadManualSignal enters or replaces a rep's input for one factor.
func (s *Store) SetLeadManualSignal(ctx context.Context, leadID ids.LeadID, in SetLeadManualSignalInput) (crmcontracts.LeadManualSignal, error) {
	if err := auth.Require(ctx, "lead", principal.ActionUpdate); err != nil {
		return crmcontracts.LeadManualSignal{}, err
	}
	bands, known := leadManualFactorBands[in.Factor]
	if !known {
		return crmcontracts.LeadManualSignal{}, &UnknownLeadFactorError{Factor: in.Factor}
	}
	points, valid := bands[in.Band]
	if !valid {
		return crmcontracts.LeadManualSignal{}, &InvalidLeadBandError{Factor: in.Factor, Band: in.Band}
	}
	// set_by is stamped from the authenticated principal, never a request
	// field: the whole value of a manual factor is knowing whose judgement
	// it was. An agent acting for a human records that human.
	actor, ok := principal.Actor(ctx)
	if !ok {
		return crmcontracts.LeadManualSignal{}, apperrors.ErrPermissionDenied
	}
	setBy := actor.UserID
	if setBy.IsZero() {
		setBy = actor.OnBehalfOf
	}
	if setBy.IsZero() {
		return crmcontracts.LeadManualSignal{}, fmt.Errorf(
			"%w: a manual scoring input needs a human behind it", apperrors.ErrPermissionDenied)
	}

	var out crmcontracts.LeadManualSignal
	err := s.tx(ctx, func(tx pgx.Tx) error {
		if err := auth.EnsureVisibleLive(ctx, tx, "lead", leadID.UUID); err != nil {
			return err
		}
		// The auto value wins (ADR-0105 §4), so a factor the model already
		// fetches cannot be hand-set: accepting it would let an estimate
		// outrank a fact one request after the rule says otherwise. A rep who
		// disagrees with a fetched fact is overruling the MODEL, which is
		// what the Commercial Judgement override on the lead is for.
		superseded, err := factorIsAutoSourced(ctx, tx, leadID, in.Factor)
		if err != nil {
			return err
		}
		if superseded {
			return &FactorAutoSourcedError{Factor: in.Factor}
		}
		// One LIVE band per factor: re-setting replaces. The superseded rows
		// stay, which is why this deletes only the live one.
		if _, err := tx.Exec(ctx,
			`DELETE FROM lead_manual_signal WHERE lead_id = $1 AND factor = $2 AND superseded_at IS NULL`,
			leadID, in.Factor); err != nil {
			return fmt.Errorf("replace manual signal: %w", err)
		}
		row := tx.QueryRow(ctx,
			`INSERT INTO lead_manual_signal (lead_id, factor, band, points, signal_kind, confidence, reason, set_by)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			 RETURNING factor, band, points, signal_kind, confidence, reason, set_by, set_at`,
			leadID, in.Factor, in.Band, points, in.SignalKind, in.Confidence, in.Reason, setBy)
		if err := scanManualSignal(row, &out); err != nil {
			return err
		}
		auditID, err := storekit.Audit(ctx, tx, "update", "lead", leadID.UUID, nil,
			map[string]any{"manual_signal": map[string]any{
				"factor": in.Factor, "band": in.Band, "points": points, "reason": in.Reason,
			}})
		if err != nil {
			return err
		}
		if err := storekit.EmitEvent(ctx, tx, auditID, leadID.UUID, crmcontracts.PublicEventLeadUpdated{
			ChangedFields: map[string]any{eventKeyDelta: map[string]any{"manual_signal": in.Factor}},
		}); err != nil {
			return err
		}
		return recomputeLeadScoreTx(ctx, tx, leadID, time.Now().UTC())
	})
	if err != nil {
		return crmcontracts.LeadManualSignal{}, err
	}
	return out, nil
}

// ClearLeadManualSignal withdraws a rep's live input for one factor.
//
// Clearing withdraws a CURRENT input. Where there is none — the auto value
// already took the factor over — this succeeds having done nothing, rather
// than 404ing at a rep for the absence of a row they did not know was gone.
// Superseded rows are history and this path never deletes them.
func (s *Store) ClearLeadManualSignal(ctx context.Context, leadID ids.LeadID, factor string) error {
	if err := auth.Require(ctx, "lead", principal.ActionUpdate); err != nil {
		return err
	}
	if _, known := leadManualFactorBands[factor]; !known {
		return &UnknownLeadFactorError{Factor: factor}
	}
	return s.tx(ctx, func(tx pgx.Tx) error {
		if err := auth.EnsureVisibleLive(ctx, tx, "lead", leadID.UUID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx,
			`DELETE FROM lead_manual_signal WHERE lead_id = $1 AND factor = $2 AND superseded_at IS NULL`,
			leadID, factor)
		if err != nil {
			return fmt.Errorf("clear manual signal: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		auditID, err := storekit.Audit(ctx, tx, "update", "lead", leadID.UUID,
			map[string]any{"manual_signal": factor}, nil)
		if err != nil {
			return err
		}
		if err := storekit.EmitEvent(ctx, tx, auditID, leadID.UUID, crmcontracts.PublicEventLeadUpdated{
			ChangedFields: map[string]any{eventKeyDelta: map[string]any{"manual_signal_cleared": factor}},
		}); err != nil {
			return err
		}
		return recomputeLeadScoreTx(ctx, tx, leadID, time.Now().UTC())
	})
}

// factorIsAutoSourced reports whether an auto value has taken this factor
// over — recorded as a superseded row naming what replaced the estimate.
func factorIsAutoSourced(ctx context.Context, tx pgx.Tx, leadID ids.LeadID, factor string) (bool, error) {
	var exists bool
	err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM lead_manual_signal
		                 WHERE lead_id = $1 AND factor = $2 AND superseded_at IS NOT NULL)`,
		leadID, factor).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check auto-sourced factor: %w", err)
	}
	return exists, nil
}

// leadManualFactors reads the live human-provided factors for the score.
func leadManualFactors(ctx context.Context, tx pgx.Tx, leadID ids.LeadID) ([]ScoreFactor, error) {
	rows, err := tx.Query(ctx,
		`SELECT factor, points FROM lead_manual_signal
		  WHERE lead_id = $1 AND superseded_at IS NULL
		  ORDER BY factor`, leadID)
	if err != nil {
		return nil, fmt.Errorf("read manual signals: %w", err)
	}
	defer rows.Close()
	var out []ScoreFactor
	for rows.Next() {
		var factor string
		var points int
		if err := rows.Scan(&factor, &points); err != nil {
			return nil, fmt.Errorf("scan manual signal: %w", err)
		}
		// Prefixed so a reader never mistakes a human's input for something
		// the system observed — the two are shown apart (AC-S7a).
		out = append(out, ScoreFactor{Factor: "manual:" + factor, Points: float64(points)})
	}
	return out, rows.Err()
}

func scanManualSignal(row pgx.Row, out *crmcontracts.LeadManualSignal) error {
	var kind string
	err := row.Scan(&out.Factor, &out.Band, &out.Points, &kind,
		&out.Confidence, &out.Reason, &out.SetBy, &out.SetAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return apperrors.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("write manual signal: %w", err)
	}
	out.SignalKind = crmcontracts.LeadManualSignalKind(kind)
	return nil
}
