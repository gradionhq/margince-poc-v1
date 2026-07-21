// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
)

// LabeledCaptureCountSince counts the activity rows the classify worker has
// stamped with capture_labeled_at since the given instant — the EXACT observed
// unit of classify work. It absorbs batching and per-item solo re-asks (a
// labeled row is one classified message no matter how many model calls it
// took), so it is the right denominator for turning served classify token
// totals into a per-message cost. RLS-scoped to the current workspace.
func (s *Store) LabeledCaptureCountSince(ctx context.Context, since time.Time) (int64, error) {
	var count int64
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT count(*) FROM activity WHERE capture_labeled_at >= $1`, since,
		).Scan(&count)
	})
	if err != nil {
		return 0, fmt.Errorf("activities: labeled capture count: %w", err)
	}
	return count, nil
}
