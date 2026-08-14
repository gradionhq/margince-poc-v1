// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

// The retained score series behind "Explain This Score" (ADR-0105/A156).
//
// The breakdown is written WITH the score, in the same transaction, and
// read back verbatim — never recomputed at read time. Behavioral factors
// decay continuously, so a decomposition rebuilt when somebody opens the
// page explains a number the record no longer carries, which is the one
// way an explanation can be worse than no explanation at all.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// appendLeadScoreHistory records one point in the series.
//
// `displayed` is what the lead row now carries — the human's number while
// a Commercial Judgement override is in force (A68/ADR-0053), else the
// machine value. `scored` is always the machine run, so `factors` sum to
// `scored.RawSum` and reconcile to `scored.Score` through the rounding and
// clamping the entry keeps visible. Storing only one number would make
// every overridden lead's explanation a claim about the wrong value.
func appendLeadScoreHistory(
	ctx context.Context,
	tx pgx.Tx,
	leadID ids.LeadID,
	displayed int,
	scored LeadScoring,
	overrideReason *string,
) error {
	// A nil slice marshals to `null`; the column is NOT NULL and a reader
	// expects a list. An empty breakdown is a real state — a cold lead with
	// no title and a neutral source scores 0 from nothing.
	factors := scored.Factors
	if factors == nil {
		factors = []ScoreFactor{}
	}
	encoded, err := json.Marshal(factors)
	if err != nil {
		return fmt.Errorf("encode lead score factors: %w", err)
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO lead_score_history (lead_id, score, score_computed, override_reason, factors, raw_sum, rounded_sum)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		leadID, displayed, scored.Score, overrideReason, encoded, scored.RawSum, scored.RoundedSum)
	if err != nil {
		return fmt.Errorf("append lead score history: %w", err)
	}
	return nil
}
