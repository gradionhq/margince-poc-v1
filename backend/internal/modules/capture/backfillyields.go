// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// BackfillYields is a completed backfill run's real volume ratios for the
// previewing connection — how many messages one scan captured and how many
// people/organizations it created. The cost estimator turns these into
// expected units for a wider window.
type BackfillYields struct {
	Scanned              int64
	Captured             int64
	PeopleCreated        int64
	OrganizationsCreated int64
}

// BackfillYields returns the previewing connection's REPRESENTATIVE completed
// backfill run — the widest, most-recent one (window_months DESC, created_at
// DESC). It is connection-scoped on purpose: a workspace-wide sum would
// double-count widen re-scans (ON CONFLICT DO NOTHING) and blend one provider's
// yields into another's preview.
//
// A connection with no completed run returns the zero value and NO error —
// absence is not a failure; it routes the estimate to the work-shape floor.
// RLS-scoped to the current workspace.
func (r *Registry) BackfillYields(ctx context.Context, provider string, userID ids.UserID) (BackfillYields, error) {
	var y BackfillYields
	err := database.WithWorkspaceTx(ctx, r.pool, func(tx pgx.Tx) error {
		connID, err := r.connectionForUser(ctx, tx, provider, userID)
		if err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `
			SELECT scanned, captured, people_created, organizations_created
			FROM capture_backfill
			WHERE connection_id = $1 AND status = 'done'
			ORDER BY window_months DESC, created_at DESC
			LIMIT 1`, connID,
		).Scan(&y.Scanned, &y.Captured, &y.PeopleCreated, &y.OrganizationsCreated)
		if errors.Is(err, pgx.ErrNoRows) {
			// No completed run yet — the zero value routes to the floor.
			y = BackfillYields{}
			return nil
		}
		return err
	})
	if err != nil {
		return BackfillYields{}, fmt.Errorf("capture: backfill yields: %w", err)
	}
	return y, nil
}
