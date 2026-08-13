// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package integrations

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// ConfigPatch is a sparse update: a nil field means "leave it alone". The
// API key is deliberately absent — rotation goes through Connect, because a
// new key must be verified before it replaces a working one.
type ConfigPatch struct {
	Mode             *string
	Preset           *string
	Categories       *[]string
	AutomaticCreate  *bool
	AutomaticImport  *bool
	RefreshAfterDays *int
	DailyRunLimit    *int
	Budgets          *[]PoolBudget
}

// UpdateConfig patches the saved policy. The new version affects FUTURE runs
// only: a run already queued carries its own frozen snapshot, so widening the
// categories here cannot retroactively authorize a purchase (PI-AC-2).
//
// ifMatch is the caller's last-seen version. Zero means unconditional, which
// the contract permits when the header is absent; a mismatch is version skew
// rather than a silent overwrite of somebody else's edit.
//
// Zero is the ABSENCE of a precondition, never a version: the column starts at
// 1 and no row can carry zero. A transport handing zero because it could not
// read the caller's header would therefore be promoting a conditional write to
// an unconditional one — the exact overwrite the caller was preventing — so
// the transport refuses such a header rather than passing it here. A negative
// value cannot come from any row either, and is refused outright.
func (s *Store) UpdateConfig(ctx context.Context, name string, patch ConfigPatch, ifMatch int64) (Connection, error) {
	if err := auth.RequireHuman(ctx); err != nil {
		return Connection{}, err
	}
	if err := auth.Require(ctx, objectIntegrations, principal.ActionUpdate); err != nil {
		return Connection{}, err
	}
	if ifMatch < 0 {
		return Connection{}, &NegativeVersionError{Version: ifMatch}
	}
	desc, err := s.registry.Descriptor(name)
	if err != nil {
		return Connection{}, apperrors.ErrNotFound
	}

	var out Connection
	err = s.db.Tx(ctx, func(tx pgx.Tx) error {
		if err := storekit.LockWriteIdentity(ctx, tx, "provider_connection", name); err != nil {
			return err
		}
		current, err := s.readOne(ctx, tx, name)
		if err != nil {
			return err
		}
		if ifMatch != 0 && current.Version != ifMatch {
			return apperrors.ErrVersionSkew
		}

		merged := applyPatch(current, patch)
		if err := desc.ValidateSelection(merged.Preset, categoriesFrom(merged.Categories)); err != nil {
			return &UnsellableSelectionError{Reason: err.Error()}
		}
		if merged.Mode != "automatic_on_create" && merged.Mode != "on_demand" {
			return &InvalidModeError{Mode: merged.Mode}
		}
		for _, b := range merged.Budgets {
			if !knownPool(desc, b.Pool) {
				return &UnknownPoolError{Provider: desc.Name, Pool: b.Pool}
			}
		}

		if _, err := tx.Exec(ctx, `
			UPDATE provider_connection
			   SET mode = $2, preset = $3, automatic_individual_create = $4,
			       automatic_import = $5, categories = $6,
			       refresh_after_days = $7, daily_run_limit = $8
			 WHERE provider = $1`,
			name, merged.Mode, merged.Preset, merged.AutomaticCreate,
			merged.AutomaticImport, merged.Categories,
			merged.RefreshAfterDays, merged.DailyRunLimit); err != nil {
			return fmt.Errorf("integrations: updating the connection: %w", err)
		}
		if patch.Budgets != nil {
			if err := s.writeBudgets(ctx, tx, name, merged.Budgets, emptyCredits()); err != nil {
				return err
			}
		}
		if _, err := storekit.Audit(ctx, tx, "update", "provider_connection", uuidOf(current.id),
			map[string]any{auditKeyMode: current.Mode, auditKeyPreset: current.Preset},
			map[string]any{auditKeyMode: merged.Mode, auditKeyPreset: merged.Preset}); err != nil {
			return err
		}
		conns, err := s.loadConnections(ctx, tx)
		if err != nil {
			return err
		}
		out = conns[name]
		return nil
	})
	if err != nil {
		return Connection{}, err
	}
	return out, nil
}

// DeleteProviderData removes retained provider claims and the identifying
// metadata on this provider's runs. It is deliberately separate from
// disconnect: stopping the flow of data and destroying what was already
// bought are two different decisions, and a customer may want either without
// the other (PI-AC-6).
//
// The spend ledger survives, detached: what the installation paid is an
// accounting fact about the installation, and once the identifying columns
// beside it are gone it names nobody.
func (s *Store) DeleteProviderData(ctx context.Context, name string) error {
	if err := auth.RequireHuman(ctx); err != nil {
		return err
	}
	if err := auth.Require(ctx, objectIntegrations, principal.ActionDelete); err != nil {
		return err
	}
	if _, err := s.registry.Adapter(name); err != nil {
		return apperrors.ErrNotFound
	}
	return s.db.Tx(ctx, func(tx pgx.Tx) error {
		// The claims belong to the owning domain, so the domain deletes them:
		// integrations does not write another module's table. compose supplies
		// this callback from people (see doc.go); with no domain bound there
		// are no claims to delete either.
		if s.deleteClaims != nil {
			if _, err := s.deleteClaims(ctx, tx, name); err != nil {
				return fmt.Errorf("integrations: deleting provider claims: %w", err)
			}
		}
		// The run rows stay as the spend ledger, but they must stop naming
		// anybody. person_id goes first: a row saying "we bought data about
		// this person on this date" is data about that person, and leaving it
		// while deleting the values would be a scrub in name only. The
		// fingerprint is derived from their identifiers, the job id would let
		// the provider be re-asked for the same answer, and the snapshot can
		// carry identifying configuration.
		//
		// What survives is what the installation spent: the state, the cost
		// and the dates, now attached to nobody. The subject-shape check
		// permits this because it constrains person_id only when subject_kind
		// still says 'person'; a scrubbed row declares no subject.
		if _, err := tx.Exec(ctx, `
			UPDATE provider_run
			   SET person_id = NULL, subject_kind = 'scrubbed',
			       input_fingerprint = '', provider_job_id = NULL,
			       requested_by = NULL,
			       configuration_snapshot = '{}'::jsonb
			 WHERE provider = $1`, name); err != nil {
			return fmt.Errorf("integrations: scrubbing run metadata: %w", err)
		}
		if _, err := storekit.LogSystem(ctx, tx, "provider_data_deleted",
			map[string]any{auditKeyProvider: name}); err != nil {
			return err
		}
		return nil
	})
}

// readOne loads one connection's mutable policy under the caller's lock.
func (s *Store) readOne(ctx context.Context, tx pgx.Tx, name string) (currentConfig, error) {
	var c currentConfig
	err := tx.QueryRow(ctx, `
		SELECT id::text, mode, preset, automatic_individual_create, automatic_import,
		       categories, refresh_after_days, daily_run_limit, version
		  FROM provider_connection WHERE provider = $1`, name).
		Scan(&c.id, &c.Mode, &c.Preset, &c.AutomaticCreate, &c.AutomaticImport,
			&c.Categories, &c.RefreshAfterDays, &c.DailyRunLimit, &c.Version)
	if errors.Is(err, pgx.ErrNoRows) {
		return currentConfig{}, apperrors.ErrNotFound
	}
	if err != nil {
		return currentConfig{}, fmt.Errorf("integrations: reading the connection: %w", err)
	}
	return c, nil
}

type currentConfig struct {
	id               *string
	Mode             string
	Preset           string
	Categories       []string
	AutomaticCreate  bool
	AutomaticImport  bool
	RefreshAfterDays *int
	DailyRunLimit    *int
	Version          int64
	Budgets          []PoolBudget
}

// applyPatch folds a sparse patch onto the current policy.
func applyPatch(current currentConfig, patch ConfigPatch) currentConfig {
	out := current
	if patch.Mode != nil {
		out.Mode = *patch.Mode
	}
	if patch.Preset != nil {
		out.Preset = *patch.Preset
	}
	if patch.Categories != nil {
		out.Categories = *patch.Categories
	}
	if patch.AutomaticCreate != nil {
		out.AutomaticCreate = *patch.AutomaticCreate
	}
	if patch.AutomaticImport != nil {
		out.AutomaticImport = *patch.AutomaticImport
	}
	if patch.RefreshAfterDays != nil {
		out.RefreshAfterDays = patch.RefreshAfterDays
	}
	if patch.DailyRunLimit != nil {
		out.DailyRunLimit = patch.DailyRunLimit
	}
	if patch.Budgets != nil {
		out.Budgets = *patch.Budgets
	}
	return out
}
