// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"context"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// RunModelUsage is one route/model slice within a correlated product task.
// ConfiguredModel says what routing selected; ServedModel preserves the
// provider's own answer about which model actually handled the request.
type RunModelUsage struct {
	Task, Tier, Provider, ConfiguredModel, ServedModel string
	CallAttempts                                       int
	TokensIn, TokensOut                                int64
	CachedTokens, CacheWriteTokens, ReasoningTokens    int64
	LatencyMS, EstimatedCostMicroUSD                   int64
	UnpricedCalls                                      int
	LastUsedAt                                         time.Time
}

// RunSummary is the cumulative, price-on-read account for one correlation id.
// Cost remains an estimate and UnpricedCalls makes a partial estimate explicit.
type RunSummary struct {
	Currency                       string
	CallAttempts, UnpricedCalls    int
	TokensIn, TokensOut, LatencyMS int64
	EstimatedCostMicroUSD          int64
	Models                         []RunModelUsage
}

type runCall struct {
	Task, Tier, Provider, ConfiguredModel, ServedModel string
	TokensIn, TokensOut                                int64
	CachedTokens, CacheWriteTokens, ReasoningTokens    int64
	LatencyMS                                          int64
	OccurredAt                                         time.Time
	RateFound                                          bool
	Rate                                               ModelRate
}

// RunTransparency reads AI telemetry for a product-owned correlation id.
// It intentionally grants the same create fallback as the unbound onboarding
// dossier itself: before an anchor company exists, its installer may still
// inspect the model calls that their own setup action caused.
type RunTransparency struct{ pool *pgxpool.Pool }

// NewRunTransparency constructs the RLS-bound correlated-run reader.
func NewRunTransparency(pool *pgxpool.Pool) *RunTransparency {
	return &RunTransparency{pool: pool}
}

// Get returns the complete attempt and cost account for one correlation id.
func (s *RunTransparency) Get(ctx context.Context, correlationID ids.UUID) (RunSummary, error) {
	if err := auth.Require(ctx, "organization", principal.ActionRead); err != nil {
		if createErr := auth.Require(ctx, "organization", principal.ActionCreate); createErr != nil {
			return RunSummary{}, createErr
		}
	}
	calls := []runCall{}
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT c.task, c.tier, c.provider, c.model_id, c.served_model,
				c.tokens_in, c.tokens_out, c.cached_tokens, c.cache_write_tokens,
				c.reasoning_tokens, c.latency_ms, c.occurred_at,
				(r.id IS NOT NULL),
				COALESCE(r.input_per_mtok_microusd, 0),
				COALESCE(r.output_per_mtok_microusd, 0),
				COALESCE(r.cache_read_per_mtok_microusd, 0),
				COALESCE(r.cache_write_per_mtok_microusd, 0),
				COALESCE(r.effective_date, c.occurred_at::date)
			FROM ai_call c
			LEFT JOIN LATERAL (
				SELECT mr.* FROM ai_model_rate mr
				WHERE mr.provider = c.provider AND mr.model_id = c.model_id
				  AND mr.effective_date <= c.occurred_at::date
				ORDER BY mr.effective_date DESC LIMIT 1
			) r ON true
			WHERE c.correlation_id = $1
			ORDER BY c.occurred_at ASC, c.attempt ASC`, correlationID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var call runCall
			if err := rows.Scan(&call.Task, &call.Tier, &call.Provider, &call.ConfiguredModel,
				&call.ServedModel, &call.TokensIn, &call.TokensOut, &call.CachedTokens,
				&call.CacheWriteTokens, &call.ReasoningTokens, &call.LatencyMS,
				&call.OccurredAt, &call.RateFound, &call.Rate.InputPerMTokMicroUSD,
				&call.Rate.OutputPerMTokMicroUSD, &call.Rate.CacheReadPerMTokMicroUSD,
				&call.Rate.CacheWritePerMTokMicroUSD, &call.Rate.EffectiveDate); err != nil {
				return err
			}
			calls = append(calls, call)
		}
		return rows.Err()
	})
	if err != nil {
		return RunSummary{}, err
	}
	return summarizeRun(calls), nil
}

type runModelKey struct {
	task, tier, provider, configuredModel, servedModel string
}

func summarizeRun(calls []runCall) RunSummary {
	summary := RunSummary{Currency: "USD", Models: []RunModelUsage{}}
	grouped := make(map[runModelKey]*RunModelUsage)
	for _, call := range calls {
		key := runModelKey{call.Task, call.Tier, call.Provider, call.ConfiguredModel, call.ServedModel}
		usage := grouped[key]
		if usage == nil {
			usage = &RunModelUsage{
				Task: call.Task, Tier: call.Tier, Provider: call.Provider,
				ConfiguredModel: call.ConfiguredModel, ServedModel: call.ServedModel,
			}
			grouped[key] = usage
		}
		usage.CallAttempts++
		usage.TokensIn += call.TokensIn
		usage.TokensOut += call.TokensOut
		usage.CachedTokens += call.CachedTokens
		usage.CacheWriteTokens += call.CacheWriteTokens
		usage.ReasoningTokens += call.ReasoningTokens
		usage.LatencyMS += call.LatencyMS
		if call.OccurredAt.After(usage.LastUsedAt) {
			usage.LastUsedAt = call.OccurredAt
		}
		if call.RateFound {
			cost := PriceCall(
				Usage{
					TokensIn: int(call.TokensIn), TokensOut: int(call.TokensOut),
					CachedTokens: int(call.CachedTokens), CacheWriteTokens: int(call.CacheWriteTokens),
				},
				call.Rate,
			)
			usage.EstimatedCostMicroUSD += cost
			summary.EstimatedCostMicroUSD += cost
		} else if call.TokensIn+call.TokensOut > 0 {
			usage.UnpricedCalls++
			summary.UnpricedCalls++
		}
		summary.CallAttempts++
		summary.TokensIn += call.TokensIn
		summary.TokensOut += call.TokensOut
		summary.LatencyMS += call.LatencyMS
	}
	for _, usage := range grouped {
		summary.Models = append(summary.Models, *usage)
	}
	sort.Slice(summary.Models, func(i, j int) bool {
		if summary.Models[i].LastUsedAt.Equal(summary.Models[j].LastUsedAt) {
			return summary.Models[i].Task < summary.Models[j].Task
		}
		return summary.Models[i].LastUsedAt.Before(summary.Models[j].LastUsedAt)
	})
	return summary
}
