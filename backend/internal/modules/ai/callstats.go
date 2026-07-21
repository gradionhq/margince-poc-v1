// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
)

// ServedTaskTotal is one (task, tier, provider, model) slice of served
// ai_call history — the token buckets summed and the call count, conditioned
// on the model that ACTUALLY ran. The cost estimator prices each slice at the
// model that will run it and scales it by expected units.
//
// "Served" means the provider actually ran the model and spent tokens: NOT
// cache_hit AND tokens_in > 0 AND the terminal was not a provider failure. A
// success (error_sentinel IS NULL) qualifies, and so does 'metering_failed' —
// that terminal marks a call the model ANSWERED (tokens were spent) where only
// the usage-meter write failed (see callstore.go's errMeteringFailed), so
// excluding it would understate the per-unit cost. A cache hit, a zero-token
// row, or a genuine provider error (which spent nothing billable here) is not a
// billable model invocation and must not skew the per-unit cost.
type ServedTaskTotal struct {
	Task     Task
	Tier     Tier
	Provider string
	ModelID  string

	TokensIn         int64
	CachedTokens     int64
	CacheWriteTokens int64
	TokensOut        int64
	Calls            int64
}

// ServedTaskTotals returns the served ai_call slices for tasks since the given
// instant, grouped by (task, tier, provider, model). RLS-scoped: it observes
// only the current workspace's calls. An empty task set returns no rows.
func (s *CallReadStore) ServedTaskTotals(ctx context.Context, tasks []Task, since time.Time) ([]ServedTaskTotal, error) {
	// tier is carried so a since-departed slice can be repriced at the current
	// binding of its OWN tier (recorded since migration 0088), not the ladder
	// head — a routine cheap_cloud swap must not reprice the departed cloud
	// slice at the $0 local head.
	const q = `
		SELECT task, tier, provider, model_id,
		       sum(tokens_in), sum(cached_tokens), sum(cache_write_tokens),
		       sum(tokens_out), count(*)
		FROM ai_call
		WHERE occurred_at >= $1 AND NOT cache_hit
		      AND (error_sentinel IS NULL OR error_sentinel = 'metering_failed')
		      AND tokens_in > 0 AND task = ANY($2)
		GROUP BY task, tier, provider, model_id`

	taskStrings := make([]string, len(tasks))
	for i, t := range tasks {
		taskStrings[i] = string(t)
	}

	var totals []ServedTaskTotal
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, q, since, taskStrings)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t ServedTaskTotal
			if err := rows.Scan(&t.Task, &t.Tier, &t.Provider, &t.ModelID,
				&t.TokensIn, &t.CachedTokens, &t.CacheWriteTokens, &t.TokensOut,
				&t.Calls); err != nil {
				return err
			}
			totals = append(totals, t)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("ai: served task totals: %w", err)
	}
	return totals, nil
}
