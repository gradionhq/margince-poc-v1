// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The workspace capture settings (CAP-PARAM-7, ADR-0072/A118): the
// captured-organization auto-enrich posture — workspace-shared config every
// role reads and only admin/ops changes (the `capture_settings` RBAC object).
// The value lives on identity's workspace row (a ratified single-column
// cross-store config write, tableownership_test — the same shape overlay's
// x_sor_mode uses); the write is AUDIT-ONLY (no event stream, EVT-NOEVT-3: the
// closed event catalog defines no capture-settings verb).

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// captureSettingsObject is the RBAC object gating the capture-settings surface.
const captureSettingsObject = "capture_settings"

// Settings is the workspace-shared capture posture (the wire shape).
type Settings struct {
	AutoEnrich bool
}

// SettingsStore is the store over the workspace capture posture.
type SettingsStore struct {
	pool *pgxpool.Pool
}

// NewSettings builds the capture-settings store over the pool.
func NewSettings(pool *pgxpool.Pool) *SettingsStore { return &SettingsStore{pool: pool} }

// Get reads the workspace's capture settings. Read is granted to every role
// (a rep needs to see whether auto-enrich is on), gated by the capture_settings
// object.
func (s *SettingsStore) Get(ctx context.Context) (Settings, error) {
	if err := auth.Require(ctx, captureSettingsObject, principal.ActionRead); err != nil {
		return Settings{}, err
	}
	var out Settings
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT capture_auto_enrich FROM workspace WHERE id = $1`,
			storekit.MustWorkspace(ctx)).Scan(&out.AutoEnrich)
	})
	if err != nil {
		return Settings{}, fmt.Errorf("capture: reading settings: %w", err)
	}
	return out, nil
}

// Update applies a sparse capture-settings patch (admin/ops, human-only). A
// nil field is left unchanged; a real change is an audit-only write against the
// workspace id. Returns the settings after the write (or the unchanged
// settings when the patch is empty).
func (s *SettingsStore) Update(ctx context.Context, autoEnrich *bool) (Settings, error) {
	if err := auth.Require(ctx, captureSettingsObject, principal.ActionUpdate); err != nil {
		return Settings{}, err
	}
	var out Settings
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		wsID := storekit.MustWorkspace(ctx)
		var before bool
		if err := tx.QueryRow(ctx,
			`SELECT capture_auto_enrich FROM workspace WHERE id = $1`, wsID).Scan(&before); err != nil {
			return fmt.Errorf("read settings before update: %w", err)
		}
		out.AutoEnrich = before
		if autoEnrich == nil || *autoEnrich == before {
			// Nothing to change — no write, no audit row (an idempotent PATCH
			// is a no-op, not a spurious audit entry).
			return nil
		}
		if _, err := tx.Exec(ctx,
			`UPDATE workspace SET capture_auto_enrich = $2 WHERE id = $1`, wsID, *autoEnrich); err != nil {
			return fmt.Errorf("update capture_auto_enrich: %w", err)
		}
		out.AutoEnrich = *autoEnrich
		// Audit-only by ratification (EVT-NOEVT-3): the closed event catalog
		// defines no capture-settings verb; the posture is workspace config,
		// the same ruling as the fx_rate/product rate-card writes.
		if _, err := storekit.Audit(ctx, tx, "update", captureSettingsObject, wsID,
			map[string]any{"auto_enrich": before}, map[string]any{"auto_enrich": *autoEnrich}); err != nil {
			return fmt.Errorf("audit capture settings update: %w", err)
		}
		return nil
	})
	if err != nil {
		return Settings{}, err
	}
	return out, nil
}
