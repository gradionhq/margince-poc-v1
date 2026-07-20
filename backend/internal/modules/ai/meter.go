// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/database"
)

// Usage is one model call's metering record (§6): who spent, on what,
// how much, and whether the cache saved the call.
type Usage struct {
	Task      Task
	Tier      Tier
	TokensIn  int
	TokensOut int
	Cached    bool
	// CachedTokens / ReasoningTokens are the itemized counts a native provider
	// reports (prompt-cache reads, reasoning/thinking tokens); 0 when the
	// provider reports neither.
	CachedTokens    int
	ReasoningTokens int
	// CacheWriteTokens is the cache-creation bucket a native provider reports
	// (e.g. Anthropic's cache_creation_input_tokens) — disjoint from
	// CachedTokens (a read), already counted inside TokensIn, 0 when the
	// provider reports none. Record persists it into ai_usage.cache_write_tokens;
	// the pricer (pricing.go) is the other reader.
	CacheWriteTokens int
}

// usageStore is what the router needs from metering; the interface
// exists so router unit tests run without Postgres while the Meter
// below is the one real implementation.
type usageStore interface {
	Record(ctx context.Context, u Usage) error
	MonthTokens(ctx context.Context) (int64, error)
}

// Meter accumulates ai_usage counters per (workspace, day, task, tier).
// It rides the workspace GUC transaction like every tenant write; a
// call outside workspace context is a programming error and fails.
type Meter struct {
	pool *pgxpool.Pool
	// now is injectable so tests pin the day/month boundaries.
	now func() time.Time
}

func NewMeter(pool *pgxpool.Pool) *Meter {
	return &Meter{pool: pool, now: time.Now}
}

func (m *Meter) Record(ctx context.Context, u Usage) error {
	day := m.now().UTC().Format("2006-01-02")
	cachedHit := 0
	if u.Cached {
		cachedHit = 1
	}
	return database.WithWorkspaceTx(ctx, m.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO ai_usage (workspace_id, day, task, tier, calls, cached_hits, tokens_in, tokens_out, reasoning_tokens, cached_tokens, cache_write_tokens)
			VALUES (NULLIF(current_setting('app.workspace_id', true), '')::uuid, $1, $2, $3, 1, $4, $5, $6, $7, $8, $9)
			ON CONFLICT (workspace_id, day, task, tier) DO UPDATE SET
			  calls              = ai_usage.calls + 1,
			  cached_hits        = ai_usage.cached_hits + EXCLUDED.cached_hits,
			  tokens_in          = ai_usage.tokens_in + EXCLUDED.tokens_in,
			  tokens_out         = ai_usage.tokens_out + EXCLUDED.tokens_out,
			  reasoning_tokens   = ai_usage.reasoning_tokens + EXCLUDED.reasoning_tokens,
			  cached_tokens      = ai_usage.cached_tokens + EXCLUDED.cached_tokens,
			  cache_write_tokens = ai_usage.cache_write_tokens + EXCLUDED.cache_write_tokens`,
			day, string(u.Task), string(u.Tier), cachedHit, u.TokensIn, u.TokensOut, u.ReasoningTokens, u.CachedTokens, u.CacheWriteTokens)
		if err != nil {
			return fmt.Errorf("ai: metering: %w", err)
		}
		return nil
	})
}

// MonthTokens sums the workspace's current calendar-month spend — the
// input to the §1.3 utilization bands.
func (m *Meter) MonthTokens(ctx context.Context) (int64, error) {
	monthStart := m.now().UTC().Format("2006-01") + "-01"
	var total int64
	err := database.WithWorkspaceTx(ctx, m.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT COALESCE(SUM(tokens_in + tokens_out), 0)
			FROM ai_usage WHERE day >= $1::date`, monthStart).Scan(&total)
	})
	if err != nil {
		return 0, fmt.Errorf("ai: month tokens: %w", err)
	}
	return total, nil
}

// PremiumShare is the premium-tier fraction of tokens over the trailing
// window — the §1.3 routing-fix alarm input.
func (m *Meter) PremiumShare(ctx context.Context, window time.Duration) (share float64, alarm bool, err error) {
	since := m.now().UTC().Add(-window).Format("2006-01-02")
	var premium, total int64
	err = database.WithWorkspaceTx(ctx, m.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT
			  COALESCE(SUM(tokens_in + tokens_out) FILTER (WHERE tier = $1), 0),
			  COALESCE(SUM(tokens_in + tokens_out), 0)
			FROM ai_usage WHERE day >= $2::date`, string(TierPremium), since).Scan(&premium, &total)
	})
	if err != nil {
		return 0, false, fmt.Errorf("ai: premium share: %w", err)
	}
	if total == 0 {
		return 0, false, nil
	}
	share = float64(premium) / float64(total)
	return share, share > premiumShareAlarmThreshold, nil
}
