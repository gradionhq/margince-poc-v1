// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// ModelRateRow is one effective-dated model price for the editor surface:
// the four per-MTok buckets as USD decimal strings (the wire/UI unit), keyed
// by (provider, model_id) with the effective date. Distinct from ModelRate,
// which carries the µUSD integers the price-on-read path computes against.
type ModelRateRow struct {
	Provider      string
	ModelID       string
	InputUsd      string
	OutputUsd     string
	CacheReadUsd  string
	CacheWriteUsd string
	EffectiveDate time.Time
}

// SetModelRateInput sets one effective-dated model price. The four prices
// are USD-per-MTok decimal strings; EffectiveDate may be today or later,
// never the past (strict append-forward).
type SetModelRateInput struct {
	Provider      string
	ModelID       string
	InputUsd      string
	OutputUsd     string
	CacheReadUsd  string
	CacheWriteUsd string
	EffectiveDate time.Time
}

func (s *RateStore) todayUTC() time.Time {
	return s.clock().UTC().Truncate(24 * time.Hour)
}

// modelRateMicroUSD converts the four USD/MTok string buckets to µUSD, failing
// on the first invalid one (all typed 422s).
func modelRateMicroUSD(in SetModelRateInput) (input, output, cacheRead, cacheWrite int64, err error) {
	if input, err = UsdPerMTokToMicroUSD("input_per_mtok", in.InputUsd); err != nil {
		return
	}
	if output, err = UsdPerMTokToMicroUSD("output_per_mtok", in.OutputUsd); err != nil {
		return
	}
	if cacheRead, err = UsdPerMTokToMicroUSD("cache_read_per_mtok", in.CacheReadUsd); err != nil {
		return
	}
	cacheWrite, err = UsdPerMTokToMicroUSD("cache_write_per_mtok", in.CacheWriteUsd)
	return
}

// SetModelRate appends (or corrects, same UTC day) one effective-dated
// model price. Admin/ops-gated; append-forward (rejects a past effective
// date). Audit-only by ratification: the closed event catalog has no
// ai/pricing stream to ride (see auditOnlyWrites in writeshape_test.go).
func (s *RateStore) SetModelRate(ctx context.Context, in SetModelRateInput) (ModelRateRow, error) {
	// RBAC + pure shape validation (provider/model presence, µUSD range) BEFORE
	// acquiring a connection, so a denied (403) or malformed-shape (422) write
	// never depends on pool health or consumes a transaction. The clock-
	// dependent effective-day guard lives in the tx (writeModelRate).
	p, err := s.prepareModelRate(ctx, in)
	if err != nil {
		return ModelRateRow{}, err
	}
	var out ModelRateRow
	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var e error
		out, e = s.writeModelRate(ctx, tx, p, in.EffectiveDate)
		return e
	})
	if err != nil {
		return ModelRateRow{}, err
	}
	return out, nil
}

// SetModelRateInTx applies one effective-dated model price through a
// caller-owned transaction — the approval-effect path, where redeem-and-write
// must commit together (a failed write leaves the approval unconsumed and
// retryable). SetModelRate is the standalone-transaction wrapper.
func (s *RateStore) SetModelRateInTx(ctx context.Context, tx pgx.Tx, in SetModelRateInput) (ModelRateRow, error) {
	p, err := s.prepareModelRate(ctx, in)
	if err != nil {
		return ModelRateRow{}, err
	}
	return s.writeModelRate(ctx, tx, p, in.EffectiveDate)
}

// preparedModelRate is one shape-validated model-price write: the trimmed
// identity and the four µUSD buckets. The effective day is resolved and guarded
// in writeModelRate (clock sampled at write time).
type preparedModelRate struct {
	provider, modelID                    string
	input, output, cacheRead, cacheWrite int64
}

// prepareModelRate runs the connection-free, clock-free gates — RBAC admission,
// provider/model presence, and the USD→µUSD range conversion — returning the
// shape-validated write.
func (s *RateStore) prepareModelRate(ctx context.Context, in SetModelRateInput) (preparedModelRate, error) {
	if err := auth.Require(ctx, "ai_model_rate", principal.ActionCreate); err != nil {
		return preparedModelRate{}, err
	}
	provider := strings.TrimSpace(in.Provider)
	modelID := strings.TrimSpace(in.ModelID)
	if provider == "" {
		return preparedModelRate{}, rateInvalid("provider", "rate_provider_required", "provider is required")
	}
	if modelID == "" {
		return preparedModelRate{}, rateInvalid("model_id", "rate_model_required", "model_id is required")
	}
	input, output, cacheRead, cacheWrite, err := modelRateMicroUSD(in)
	if err != nil {
		return preparedModelRate{}, err
	}
	return preparedModelRate{
		provider: provider, modelID: modelID,
		input: input, output: output, cacheRead: cacheRead, cacheWrite: cacheWrite,
	}, nil
}

// writeModelRate does the transactional body: resolve and guard the effective
// day against a clock sampled INSIDE the tx (so a write that waited for the pool
// across UTC midnight is stored against the day it commits — append-forward
// stays true at write time), then upsert the append-forward row and audit — all
// in the caller-owned tx.
func (s *RateStore) writeModelRate(ctx context.Context, tx pgx.Tx, p preparedModelRate, effectiveDate time.Time) (ModelRateRow, error) {
	today := s.todayUTC()
	if effectiveDate.IsZero() {
		effectiveDate = today
	}
	effDate := effectiveDate.UTC().Truncate(24 * time.Hour)
	if effDate.Before(today) {
		return ModelRateRow{}, rateInvalid("effective_date", "rate_past", "effective_date cannot be in the past")
	}
	var (
		out                                 ModelRateRow
		id                                  ids.UUID
		inMicro, outMicro, crMicro, cwMicro int64
		eff                                 time.Time
		provOut, modelOut                   string
	)
	if err := tx.QueryRow(ctx, `
		INSERT INTO ai_model_rate (
			workspace_id, provider, model_id,
			input_per_mtok_microusd, output_per_mtok_microusd,
			cache_read_per_mtok_microusd, cache_write_per_mtok_microusd,
			effective_date)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (workspace_id, provider, model_id, effective_date)
		DO UPDATE SET
			input_per_mtok_microusd       = EXCLUDED.input_per_mtok_microusd,
			output_per_mtok_microusd      = EXCLUDED.output_per_mtok_microusd,
			cache_read_per_mtok_microusd  = EXCLUDED.cache_read_per_mtok_microusd,
			cache_write_per_mtok_microusd = EXCLUDED.cache_write_per_mtok_microusd
		RETURNING id, provider, model_id, input_per_mtok_microusd, output_per_mtok_microusd,
		          cache_read_per_mtok_microusd, cache_write_per_mtok_microusd, effective_date`,
		storekit.MustWorkspace(ctx), p.provider, p.modelID,
		p.input, p.output, p.cacheRead, p.cacheWrite, effDate,
	).Scan(&id, &provOut, &modelOut, &inMicro, &outMicro, &crMicro, &cwMicro, &eff); err != nil {
		return ModelRateRow{}, fmt.Errorf("upsert ai_model_rate: %w", err)
	}
	out = ModelRateRow{
		Provider: provOut, ModelID: modelOut,
		InputUsd: MicroUSDToUsdPerMTok(inMicro), OutputUsd: MicroUSDToUsdPerMTok(outMicro),
		CacheReadUsd: MicroUSDToUsdPerMTok(crMicro), CacheWriteUsd: MicroUSDToUsdPerMTok(cwMicro),
		EffectiveDate: eff,
	}
	// Audit the UTC-truncated day actually stored, not the caller's raw
	// timestamp, so the ledger is faithful to the persisted rate (matches the
	// fx_rate sibling).
	if _, err := storekit.Audit(ctx, tx, "create", "ai_model_rate", id, nil, map[string]any{
		"provider": p.provider, "model_id": p.modelID,
		"input_microusd": p.input, "output_microusd": p.output,
		"cache_read_microusd": p.cacheRead, "cache_write_microusd": p.cacheWrite,
		"date": effDate,
	}); err != nil {
		return ModelRateRow{}, fmt.Errorf("audit ai_model_rate create: %w", err)
	}
	return out, nil
}

// ListLatestModelRates returns the head of the price sheet — the latest-dated
// row per (provider, model_id), which MAY be a future-scheduled price. The
// editor's "sheet head" view, distinct from RateFor's as-of-day effective
// price (effective_date <= day). Admin/ops read gate.
func (s *RateStore) ListLatestModelRates(ctx context.Context) ([]ModelRateRow, error) {
	if err := auth.Require(ctx, "ai_model_rate", principal.ActionRead); err != nil {
		return nil, err
	}
	var rows []ModelRateRow
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		r, err := tx.Query(ctx, `
			SELECT DISTINCT ON (provider, model_id)
			       provider, model_id, input_per_mtok_microusd, output_per_mtok_microusd,
			       cache_read_per_mtok_microusd, cache_write_per_mtok_microusd, effective_date
			FROM ai_model_rate
			ORDER BY provider, model_id, effective_date DESC`)
		if err != nil {
			return fmt.Errorf("list ai_model_rate: %w", err)
		}
		defer r.Close()
		rows, err = scanModelRateRows(r)
		return err
	})
	return rows, err
}

// ModelRateHistory returns every effective-dated row for one model, newest
// first (read-only history). Admin/ops read gate.
func (s *RateStore) ModelRateHistory(ctx context.Context, provider, modelID string) ([]ModelRateRow, error) {
	if err := auth.Require(ctx, "ai_model_rate", principal.ActionRead); err != nil {
		return nil, err
	}
	var rows []ModelRateRow
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		r, err := tx.Query(ctx, `
			SELECT provider, model_id, input_per_mtok_microusd, output_per_mtok_microusd,
			       cache_read_per_mtok_microusd, cache_write_per_mtok_microusd, effective_date
			FROM ai_model_rate WHERE provider = $1 AND model_id = $2
			ORDER BY effective_date DESC`, strings.TrimSpace(provider), strings.TrimSpace(modelID))
		if err != nil {
			return fmt.Errorf("ai_model_rate history: %w", err)
		}
		defer r.Close()
		rows, err = scanModelRateRows(r)
		return err
	})
	return rows, err
}

func scanModelRateRows(r pgx.Rows) ([]ModelRateRow, error) {
	var out []ModelRateRow
	for r.Next() {
		var (
			row                                 ModelRateRow
			inMicro, outMicro, crMicro, cwMicro int64
		)
		if err := r.Scan(&row.Provider, &row.ModelID, &inMicro, &outMicro, &crMicro, &cwMicro, &row.EffectiveDate); err != nil {
			return nil, fmt.Errorf("scan ai_model_rate: %w", err)
		}
		row.InputUsd = MicroUSDToUsdPerMTok(inMicro)
		row.OutputUsd = MicroUSDToUsdPerMTok(outMicro)
		row.CacheReadUsd = MicroUSDToUsdPerMTok(crMicro)
		row.CacheWriteUsd = MicroUSDToUsdPerMTok(cwMicro)
		out = append(out, row)
	}
	if err := r.Err(); err != nil {
		return nil, fmt.Errorf("iterate ai_model_rate: %w", err)
	}
	return out, nil
}
