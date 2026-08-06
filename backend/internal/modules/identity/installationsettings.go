// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// The installation-settings surface (ADR-0090/A135): the module-facing shape
// over the three settings identity owns. RBAC, validation, the freeze probe
// and the audit-only write all live on the entries — this file is only the
// read/patch shape a transport needs.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/settings"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// InstallationSettings is the installation's identity and reporting basis.
type InstallationSettings struct {
	Name         string
	Timezone     string
	BaseCurrency string
	// BaseCurrencyLocked and its reason let a client render the field
	// read-only instead of discovering the refusal by attempting a write —
	// the same information the write path would give, offered before the
	// operator types a currency they cannot save.
	BaseCurrencyLocked       bool
	BaseCurrencyLockedReason string
}

// InstallationSettingsStore reads and patches the installation settings.
type InstallationSettingsStore struct {
	pool     *pgxpool.Pool
	settings *settings.Store
}

// NewInstallationSettings builds the store over the settings mechanism. The
// pool is held for the lock probe, which asks a question no setting read can
// answer: whether the currency has become immutable.
func NewInstallationSettings(pool *pgxpool.Pool, s *settings.Store) *InstallationSettingsStore {
	return &InstallationSettingsStore{pool: pool, settings: s}
}

// GetInstallation reads the three settings and the base currency's lock state.
//
// Named GetInstallation rather than Get deliberately. rbacgate_test.go resolves
// gatedness by BARE FUNCTION NAME within a package — optimistic by design, so
// it never cries wolf on dispatch it cannot resolve — which means a gated
// method here called `Get` would merge with identity's existing ungated `Get`
// (the self-scoped onboarding wizard state) and vouch for it, and for `Login`,
// whose waivers say plainly that neither has a principal to gate yet. A
// distinct name keeps those two honestly reported as ungated.
func (s *InstallationSettingsStore) GetInstallation(ctx context.Context) (InstallationSettings, error) {
	name, err := settings.Get(ctx, s.settings, Name)
	if err != nil {
		return InstallationSettings{}, err
	}
	zone, err := settings.Get(ctx, s.settings, Timezone)
	if err != nil {
		return InstallationSettings{}, err
	}
	currency, err := settings.Get(ctx, s.settings, BaseCurrency)
	if err != nil {
		return InstallationSettings{}, err
	}
	locked, why, err := s.baseCurrencyLock(ctx)
	if err != nil {
		return InstallationSettings{}, err
	}
	return InstallationSettings{
		Name: name, Timezone: zone, BaseCurrency: currency,
		BaseCurrencyLocked: locked, BaseCurrencyLockedReason: why,
	}, nil
}

// baseCurrencyLock asks the entry's own probe, so the answer the read reports
// and the answer the write enforces come from one place. A read with the probe
// unwired reports "changeable", which is what the write would then do — the
// two agree even when the wiring is wrong, and the fitness gate catches the
// wiring.
func (s *InstallationSettingsStore) baseCurrencyLock(ctx context.Context) (bool, string, error) {
	var locked bool
	var why string
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var probeErr error
		locked, why, probeErr = BaseCurrency.Frozen(ctx, tx)
		return probeErr
	})
	if err != nil {
		return false, "", fmt.Errorf("identity: reading base-currency lock state: %w", err)
	}
	return locked, why, nil
}

// UpdateInstallation applies a sparse patch. Named for the same reason as
// GetInstallation above. A nil field is left unchanged; an unchanged value
// writes nothing and audits nothing. Returns the settings after the write.
//
// The update gate is taken here, before any branch, so an empty patch is
// refused for a caller who may not write rather than answered from the read
// gate alone.
//
// Every field commits in ONE transaction, together with a mirror write onto
// the `workspace` columns. The mirror is TRANSITIONAL and load-bearing until
// it goes: roll-ups, FX conversion, quota attainment and the report builder
// still read workspace.base_currency and workspace.timezone directly. Without
// it, this surface would report a base currency that nothing computes in —
// changing it would move the number on this screen and leave every roll-up on
// the old basis, which is worse than not offering the control. The mirror
// retires with the columns, once those readers move (issue #521).
func (s *InstallationSettingsStore) UpdateInstallation(ctx context.Context, name, zone, currency *string) (InstallationSettings, error) {
	if err := auth.Require(ctx, installationSettingsObject, principal.ActionUpdate); err != nil {
		return InstallationSettings{}, err
	}
	// The mirror statement is a full literal per field rather than a built
	// column name: nothing here is caller-supplied, and spelling it out keeps
	// the table-ownership gate able to read the target out of the SQL.
	patch := []struct {
		entry  *settings.Entry[string]
		value  *string
		mirror string
	}{
		{Name, name, `UPDATE workspace SET name = $1 WHERE id = $2`},
		{Timezone, zone, `UPDATE workspace SET timezone = $1 WHERE id = $2`},
		{BaseCurrency, currency, `UPDATE workspace SET base_currency = $1 WHERE id = $2`},
	}
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		for _, w := range patch {
			if w.value == nil {
				continue
			}
			raw, err := json.Marshal(*w.value)
			if err != nil {
				return fmt.Errorf("identity: encoding %s: %w", w.entry.Key(), err)
			}
			if err := s.settings.SetRawTx(ctx, tx, w.entry.Key(), raw); err != nil {
				return err
			}
			// The mirror, in the same transaction as the setting it copies:
			// the two can never disagree, which is the only property that
			// makes keeping both tolerable.
			if _, err := tx.Exec(ctx, w.mirror, *w.value, storekit.MustWorkspace(ctx)); err != nil {
				return fmt.Errorf("identity: mirroring %s onto the workspace row: %w", w.entry.Key(), err)
			}
		}
		return nil
	})
	if err != nil {
		return InstallationSettings{}, err
	}
	return s.GetInstallation(ctx)
}
