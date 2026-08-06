// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// The installation-settings surface (ADR-0090/A135): the module-facing shape
// over the three settings identity owns. RBAC, validation, the freeze probe
// and the audit-only write all live on the entries — this file is only the
// read/patch shape a transport needs.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/platform/auth"
	"github.com/gradionhq/margince/backend/internal/platform/database"
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
// GetInstallation above. A nil field is left unchanged; an unchanged
// value writes nothing and audits nothing. Returns the settings after the
// write.
//
// The update gate is taken here, before any branch, so an empty patch is
// refused for a caller who may not write rather than answered from the read
// gate alone.
func (s *InstallationSettingsStore) UpdateInstallation(ctx context.Context, name, zone, currency *string) (InstallationSettings, error) {
	if err := auth.Require(ctx, installationSettingsObject, principal.ActionUpdate); err != nil {
		return InstallationSettings{}, err
	}
	// Applied one at a time, each in its own transaction. A patch touching
	// several settings is not atomic across them — stated rather than implied,
	// because the alternative (one transaction spanning the set) would need
	// the settings store to expose a batch write whose only caller is here,
	// and a partial patch leaves every value it did write valid and audited.
	for _, w := range []struct {
		entry *settings.Entry[string]
		value *string
	}{
		{Name, name},
		{Timezone, zone},
		{BaseCurrency, currency},
	} {
		if w.value == nil {
			continue
		}
		if err := settings.Set(ctx, s.settings, w.entry, *w.value); err != nil {
			return InstallationSettings{}, err
		}
	}
	return s.GetInstallation(ctx)
}
