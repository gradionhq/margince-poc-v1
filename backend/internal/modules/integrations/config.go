// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package integrations

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/keyvault"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/provider"
)

// resolvedConfig is a saved policy that has been checked against what the
// provider actually sells.
type resolvedConfig struct {
	Mode             string
	Preset           string
	Categories       []string
	AutomaticCreate  bool
	AutomaticImport  bool
	RefreshAfterDays *int
	DailyRunLimit    *int
	Budgets          []PoolBudget
}

// resolveConfig turns an optional request body into a policy this provider
// can honour. An omitted body is not an error: it resolves to
// automatic-on-create with the descriptor's default preset (PI-AC-1), which
// is what a customer who just connected a provider expects to happen.
func resolveConfig(desc provider.Descriptor, in *ConfigInput) (resolvedConfig, error) {
	out := resolvedConfig{
		Mode:            string(defaultMode),
		Preset:          desc.DefaultPreset,
		AutomaticCreate: true,
		AutomaticImport: false,
	}
	out.Categories = categoryStrings(desc.ResolvePreset(desc.DefaultPreset, nil))

	if in == nil {
		return out, nil
	}
	if in.Mode != "" {
		if in.Mode != "automatic_on_create" && in.Mode != "on_demand" {
			return resolvedConfig{}, &InvalidModeError{Mode: in.Mode}
		}
		out.Mode = in.Mode
	}
	if in.Preset != "" {
		out.Preset = in.Preset
	}
	if len(in.Categories) > 0 {
		out.Categories = in.Categories
	} else if in.Preset != "" && in.Preset != "custom" {
		out.Categories = categoryStrings(desc.ResolvePreset(in.Preset, nil))
	}
	if in.AutomaticCreate != nil {
		out.AutomaticCreate = *in.AutomaticCreate
	}
	if in.AutomaticImport != nil {
		out.AutomaticImport = *in.AutomaticImport
	}
	out.RefreshAfterDays = in.RefreshAfterDays
	out.DailyRunLimit = in.DailyRunLimit
	out.Budgets = in.Budgets

	// The descriptor is the authority on what may be selected: JSON Schema
	// admits any string map here, because the vocabulary belongs to the
	// provider rather than to the contract.
	if err := desc.ValidateSelection(out.Preset, categoriesFrom(out.Categories)); err != nil {
		return resolvedConfig{}, &UnsellableSelectionError{Reason: err.Error()}
	}
	for _, b := range out.Budgets {
		if !knownPool(desc, b.Pool) {
			return resolvedConfig{}, &UnknownPoolError{Provider: desc.Name, Pool: b.Pool}
		}
	}
	return out, nil
}

func knownPool(desc provider.Descriptor, pool string) bool {
	for _, p := range desc.CreditPools {
		if string(p) == pool {
			return true
		}
	}
	return false
}

// connectionRow is the identity of the row that was written, plus the
// credential handle it DISPLACED.
//
// displacedRef is load-bearing on a key rotation: the row now names the new
// secret, so nothing points at the old one any more. Left alone it would sit
// sealed in the vault forever — a credential the customer believes they
// replaced, unreferenced and unreachable, which is exactly the thing a vault
// exists to not do.
type connectionRow struct {
	id           *string
	displacedRef *string
}

// replaceConnection upserts the connection and marks it connected. A rotation
// bumps the execution epoch for the same reason a disconnect does: a run
// admitted under the old key must not finish against the new one.
func (s *Store) replaceConnection(ctx context.Context, tx pgx.Tx, name string, ref keyvault.Ref, cfg resolvedConfig) (connectionRow, error) {
	// Read the outgoing handle under the lock the caller already holds, before
	// the upsert overwrites it. RETURNING cannot give us this: it reports the
	// row's state AFTER the write.
	var displaced *string
	if err := tx.QueryRow(ctx,
		`SELECT credential_ref FROM provider_connection WHERE provider = $1`, name).Scan(&displaced); err != nil &&
		!errors.Is(err, pgx.ErrNoRows) {
		return connectionRow{}, fmt.Errorf("integrations: reading the outgoing credential handle: %w", err)
	}

	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO provider_connection
		  (provider, status, mode, preset, automatic_individual_create, automatic_import,
		   categories, refresh_after_days, daily_run_limit, credential_ref,
		   connected_by, connected_at, last_verified_at)
		VALUES ($1, 'connected', $2, $3, $4, $5, $6, $7, $8, $9, $10, now(), now())
		ON CONFLICT (provider) DO UPDATE SET
		  status = 'connected',
		  mode = EXCLUDED.mode,
		  preset = EXCLUDED.preset,
		  automatic_individual_create = EXCLUDED.automatic_individual_create,
		  automatic_import = EXCLUDED.automatic_import,
		  categories = EXCLUDED.categories,
		  refresh_after_days = EXCLUDED.refresh_after_days,
		  daily_run_limit = EXCLUDED.daily_run_limit,
		  credential_ref = EXCLUDED.credential_ref,
		  connected_by = EXCLUDED.connected_by,
		  connected_at = COALESCE(provider_connection.connected_at, now()),
		  last_verified_at = now(),
		  last_safe_status_code = NULL,
		  execution_epoch = provider_connection.execution_epoch + 1
		RETURNING id::text`,
		name, cfg.Mode, cfg.Preset, cfg.AutomaticCreate, cfg.AutomaticImport,
		cfg.Categories, cfg.RefreshAfterDays, cfg.DailyRunLimit, string(ref),
		actorID(ctx)).Scan(&id)
	if err != nil {
		return connectionRow{}, fmt.Errorf("integrations: writing the connection: %w", err)
	}
	// A rotation onto the same handle displaces nothing.
	if displaced != nil && *displaced == string(ref) {
		displaced = nil
	}
	return connectionRow{id: &id, displacedRef: displaced}, nil
}

// writeBudgets replaces the per-pool budget rows and stamps the balance the
// verification call just observed, so the first run's low-balance check has
// something real to compare against.
func (s *Store) writeBudgets(ctx context.Context, tx pgx.Tx, name string, budgets []PoolBudget, credits provider.Credits) error {
	var connID string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM provider_connection WHERE provider = $1`, name).Scan(&connID); err != nil {
		return fmt.Errorf("integrations: resolving the connection: %w", err)
	}
	byPool := map[string]PoolBudget{}
	for _, b := range budgets {
		byPool[b.Pool] = b
	}
	desc, err := s.registry.Descriptor(name)
	if err != nil {
		return err
	}
	readAt := credits.ReadAt
	if readAt.IsZero() {
		readAt = s.now().UTC()
	}
	for _, pool := range desc.CreditPools {
		b := byPool[string(pool)]
		var balance *int
		var stamp *time.Time
		if v, ok := credits.Balances[pool]; ok {
			balance = &v
			stamp = &readAt
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO provider_connection_budget
			  (connection_id, pool, monthly_ceiling, pause_below_balance, last_known_balance, balance_read_at)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (connection_id, pool) DO UPDATE SET
			  monthly_ceiling = EXCLUDED.monthly_ceiling,
			  pause_below_balance = EXCLUDED.pause_below_balance,
			  last_known_balance = COALESCE(EXCLUDED.last_known_balance, provider_connection_budget.last_known_balance),
			  balance_read_at = COALESCE(EXCLUDED.balance_read_at, provider_connection_budget.balance_read_at)`,
			connID, string(pool), b.MonthlyCeiling, b.PauseBelowBalance, balance, stamp); err != nil {
			return fmt.Errorf("integrations: writing a budget: %w", err)
		}
	}
	return nil
}

// actorID names the human who connected, for the connected_by column. A
// system principal has no app_user behind it and stores NULL.
func actorID(ctx context.Context) *string {
	actor, ok := principal.Actor(ctx)
	if !ok || actor.UserID.IsZero() {
		return nil
	}
	s := actor.UserID.String()
	return &s
}

func uuidOf(id *string) ids.UUID {
	if id == nil {
		return ids.UUID{}
	}
	parsed, err := ids.Parse(*id)
	if err != nil {
		return ids.UUID{}
	}
	return parsed
}
