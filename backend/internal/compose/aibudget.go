// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The seat-derived AI budget (ai-operational-spec §1.3 / 09 §2.4): a
// workspace's monthly token pool is its FULL seats × 6M base × 2
// safety. Composed here because the policy joins ai (the guardrail)
// to identity's seat table — the ai module only ever sees the
// BudgetPolicy seam.

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

const (
	perSeatBaseTokens  = 6_000_000
	budgetSafetyFactor = 2
)

// seatBudget derives the pool live: seat changes move the budget at
// the next model call, no restart. The count runs under the target
// workspace's own GUC binding — app_user is RLS-guarded like every
// tenant table, and the budget question is always about ONE tenant.
type seatBudget struct {
	pool *pgxpool.Pool
}

// NewSeatBudget is the production BudgetPolicy.
func NewSeatBudget(pool *pgxpool.Pool) ai.BudgetPolicy { return seatBudget{pool: pool} }

func (b seatBudget) MonthlyTokenBudget(ctx context.Context, workspaceID ids.WorkspaceID) (int64, error) {
	var fullSeats int64
	err := database.WithWorkspaceTx(principal.WithWorkspaceID(ctx, workspaceID.UUID), b.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT count(*) FROM app_user
			WHERE seat_type = 'full' AND status = 'active'
			  AND archived_at IS NULL AND NOT is_agent`).Scan(&fullSeats)
	})
	if err != nil {
		return 0, err
	}
	if fullSeats == 0 {
		// A workspace with no live full seat still gets the single-seat
		// floor: onboarding flows call the model before the first seat
		// settles, and zero would hard-refuse them.
		fullSeats = 1
	}
	return fullSeats * perSeatBaseTokens * budgetSafetyFactor, nil
}
