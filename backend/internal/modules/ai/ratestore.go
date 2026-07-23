// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
)

// RateStore is the ai_model_rate price sheet — the fx_rate-style
// as-of-date lookup (ADR-0067). It rides the workspace GUC transaction
// like every tenant read: RLS alone decides which workspace's rates a
// caller can see, the same as fx_rate.
type RateStore struct {
	pool *pgxpool.Pool
	// clock is the "today" source for effective-dated writes; injected so
	// append-forward date validation is deterministic in tests.
	clock func() time.Time
}

// NewRateStore constructs the RateStore over pool.
func NewRateStore(pool *pgxpool.Pool) *RateStore {
	return &RateStore{pool: pool, clock: time.Now}
}

// WithClock overrides the "today" source (tests only). Returns the store
// for chaining.
func (s *RateStore) WithClock(clock func() time.Time) *RateStore {
	s.clock = clock
	return s
}

// RateFor resolves the rate effective on day for (provider, modelID) —
// the latest row whose effective_date is on or before day, mirroring
// fx_rate's as-of-date resolution (deal_advance.go). No matching row is
// not an error: it means the call is UNPRICED, a materially different
// signal from a 0 price (price-on-read; never fabricate a price), so the
// caller gets (nil, nil) and decides what "unpriced" means to it.
func (s *RateStore) RateFor(ctx context.Context, provider, modelID string, day time.Time) (*ModelRate, error) {
	var rate ModelRate
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT provider, model_id, input_per_mtok_microusd, output_per_mtok_microusd,
			       cache_read_per_mtok_microusd, cache_write_per_mtok_microusd, effective_date
			FROM ai_model_rate
			WHERE provider = $1 AND model_id = $2 AND effective_date <= $3
			ORDER BY effective_date DESC LIMIT 1`,
			provider, modelID, day).Scan(
			&rate.Provider, &rate.ModelID, &rate.InputPerMTokMicroUSD, &rate.OutputPerMTokMicroUSD,
			&rate.CacheReadPerMTokMicroUSD, &rate.CacheWritePerMTokMicroUSD, &rate.EffectiveDate)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil //nolint:nilnil // no matching rate row IS the "unpriced" answer, not an error — price-on-read never fabricates a price
	}
	if err != nil {
		return nil, fmt.Errorf("ai: rate lookup: %w", err)
	}
	return &rate, nil
}

// CostReport prices the [from, to) window's ai_call rows against their
// as-of-date rate and sums per (calendar day, task, tier) — THE one
// money computation that runs at read time (price-on-read: the
// router/meter/adapters never compute a cost). One SQL statement: a
// LATERAL join picks each row's as-of-date rate (RateFor's same
// resolution, inlined so the whole window prices in one query instead of
// one round-trip per call), the four-bucket arithmetic mirrors PriceCall
// exactly (same floor, same truncating /1000000), and GROUP BY the
// call's UTC calendar day + task + tier rolls the window up to exactly
// AIRT-WIRE-1's /ai/usage grain — one report line per wire row, so the
// handler attaches each line to its one matching (day, task, tier) row
// instead of broadcasting a shared task total across every tier that
// task ran on (which double-counts whenever a client sums cost_est_minor
// across rows).
//
// Two kinds of row spend nothing and are never counted unpriced, because
// they are free BY CONSTRUCTION, not merely unrated: a cache_hit (served
// from the router's result cache, no provider call happened) and a row
// with zero provider usage (tokens_in = 0 AND tokens_out = 0 — a call
// that failed before the provider was ever reached). Every other row
// with no matching rate row counts into its (day, task, tier)'s
// UnpricedCalls — visible, never a silent 0 (global constraint: cost is
// transparency, never a gate).
func (s *RateStore) CostReport(ctx context.Context, from, to time.Time) ([]DayCost, error) {
	var report []DayCost
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT
			  ac.occurred_at::date AS day,
			  ac.task,
			  ac.tier,
			  COALESCE(SUM(
			    CASE
			      WHEN ac.cache_hit OR (ac.tokens_in = 0 AND ac.tokens_out = 0) THEN 0
			      WHEN r.id IS NULL THEN 0
			      ELSE (GREATEST(ac.tokens_in - ac.cached_tokens - ac.cache_write_tokens, 0) * r.input_per_mtok_microusd
			           + ac.cached_tokens * r.cache_read_per_mtok_microusd
			           + ac.cache_write_tokens * r.cache_write_per_mtok_microusd
			           + ac.tokens_out * r.output_per_mtok_microusd) / 1000000
			    END
			  ), 0) AS cost_microusd,
			  COUNT(*) FILTER (
			    WHERE NOT ac.cache_hit
			      AND NOT (ac.tokens_in = 0 AND ac.tokens_out = 0)
			      AND r.id IS NULL
			  ) AS unpriced_calls
			FROM ai_call ac
			LEFT JOIN LATERAL (
			  SELECT mr.id, mr.input_per_mtok_microusd, mr.output_per_mtok_microusd,
			         mr.cache_read_per_mtok_microusd, mr.cache_write_per_mtok_microusd
			  FROM ai_model_rate mr
			  WHERE mr.provider = ac.provider AND mr.model_id = ac.model_id
			    AND mr.effective_date <= ac.occurred_at::date
			  ORDER BY mr.effective_date DESC
			  LIMIT 1
			) r ON true
			WHERE ac.occurred_at >= $1 AND ac.occurred_at < $2
			GROUP BY ac.occurred_at::date, ac.task, ac.tier
			ORDER BY day, ac.task, ac.tier`,
			from, to)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var dc DayCost
			var task, tier string
			if err := rows.Scan(&dc.Day, &task, &tier, &dc.CostMicroUSD, &dc.UnpricedCalls); err != nil {
				return err
			}
			dc.Task = Task(task)
			dc.Tier = Tier(tier)
			report = append(report, dc)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("ai: cost report: %w", err)
	}
	return report, nil
}
