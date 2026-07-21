// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
)

// SeedWorkspaceDefaultsTx plants the starting ai_model_rate price sheet
// (SeedModelRates) for the current workspace, inside the bootstrap
// transaction (C5 atomicity: a failed seed rolls the whole tenant back —
// the same package-func pattern as automation.SeedStarterAutomationsTx /
// consent.SeedDefaultPurposesTx). now anchors every seeded row's
// effective_date — the day the workspace was provisioned is a
// reasonable, reproducible starting point for a fresh price sheet
// (ADR-0067); an operator who disagrees corrects it the same fx_rate-style
// way as any other rate row, by adding a new effective-dated row.
//
// This is the single application point for the SeedModelRates price sheet —
// every workspace gets its rates here at provisioning, and SeedModelRates
// stays the one place to edit a rate (no hand-mirrored migration copy to
// keep in sync). Idempotent (ON CONFLICT ... DO NOTHING on the table's
// (workspace_id, provider, model_id, effective_date) uniqueness): a rerun
// never double-inserts.
func SeedWorkspaceDefaultsTx(ctx context.Context, tx pgx.Tx, now time.Time) error {
	wsID := storekit.MustWorkspace(ctx)
	for _, r := range SeedModelRates(now) {
		if _, err := tx.Exec(ctx, `
			INSERT INTO ai_model_rate (
				workspace_id, provider, model_id,
				input_per_mtok_microusd, output_per_mtok_microusd,
				cache_read_per_mtok_microusd, cache_write_per_mtok_microusd,
				effective_date
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (workspace_id, provider, model_id, effective_date) DO NOTHING`,
			wsID, r.Provider, r.ModelID,
			r.InputPerMTokMicroUSD, r.OutputPerMTokMicroUSD,
			r.CacheReadPerMTokMicroUSD, r.CacheWritePerMTokMicroUSD,
			r.EffectiveDate); err != nil {
			return fmt.Errorf("ai: seed workspace default rates: %w", err)
		}
	}
	return nil
}
