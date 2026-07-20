// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Installation bootstrap (A107/ADR-0061): the composition of the
// boot-time state machine — an empty database is bootstrapped from the
// deployment configuration file, an existing singleton binds, and a
// multi-workspace database refuses to serve. Composed here because the
// seed spans modules (deals' pipeline, consent's catalog, agents'
// automations, activities' booking page) and every cross-module edge is
// injected at the root, never as a sibling import (ADR-0054).

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/automation"
	"github.com/gradionhq/margince/backend/internal/modules/consent"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// EnsureInstallation applies the boot state machine before the API
// serves: 0 active workspaces → bootstrap organization + first admin +
// seeds atomically from cfg (requires organization + bootstrap_admin);
// 1 → bind; >1 → refuse with the operator-facing invariant error.
// Restarts are idempotent — bootstrap values never reconcile into an
// existing organization.
func EnsureInstallation(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger, cfg deployconfig.Config) error {
	var create *identity.InstallationBootstrap
	if b := cfg.BootstrapAdmin; b != nil {
		if cfg.Organization.Name == "" {
			return errors.New("compose: bootstrap_admin is configured but organization.name is missing — both are required to bootstrap an empty database")
		}
		pw, err := b.Password()
		if err != nil {
			return err
		}
		create = &identity.InstallationBootstrap{
			OrganizationName: cfg.Organization.Name,
			BaseCurrency:     cfg.Organization.BaseCurrency,
			Timezone:         cfg.Organization.Timezone,
			AdminEmail:       b.Email,
			AdminName:        b.DisplayName,
			AdminPassword:    pw,
		}
	}

	wsID, created, err := identity.NewService(pool).BootstrapInstallation(ctx, create, configuredSeed(cfg.Seeds, deals.NewHandlers(pool)))
	if errors.Is(err, identity.ErrNotBootstrapped) {
		return fmt.Errorf("compose: the database holds no organization and the configuration names no bootstrap_admin — add organization + bootstrap_admin to margince.yaml for first boot: %w", err)
	}
	if err != nil {
		return err
	}
	if created {
		log.Info("installation bootstrapped", "workspace_id", wsID.String(), "organization", cfg.Organization.Name)
	} else {
		log.Info("installation bound to existing organization", "workspace_id", wsID.String())
	}
	return nil
}

// configuredSeed lays down every module's per-workspace defaults inside
// the bootstrap transaction (C5 atomicity), shaped by the deployment
// file's optional `seeds` section — an omitted key seeds the built-in
// default, so a minimal configuration behaves exactly like the
// historical bootstrap.
func configuredSeed(seeds deployconfig.Seeds, dealsH dealsHandlers) func(context.Context, pgx.Tx) error {
	return func(ctx context.Context, tx pgx.Tx) error {
		if err := seedPipeline(ctx, tx, seeds.Pipeline, dealsH); err != nil {
			return err
		}
		if err := seedConsent(ctx, tx, seeds.ConsentPurposes); err != nil {
			return err
		}
		if err := ai.SeedWorkspaceDefaultsTx(ctx, tx, time.Now().UTC()); err != nil {
			return err
		}
		if seeds.StarterAutomations == nil || *seeds.StarterAutomations {
			if err := automation.SeedStarterAutomationsTx(ctx, tx); err != nil {
				return err
			}
		}
		if seeds.BookingPage == nil || *seeds.BookingPage {
			return seedBookingPage(ctx, tx)
		}
		return nil
	}
}

func seedPipeline(ctx context.Context, tx pgx.Tx, p *deployconfig.PipelineSeed, dealsH dealsHandlers) error {
	if p == nil {
		return dealsH.SeedWorkspaceDefaultsTx(ctx, tx)
	}
	open := make([]deals.StageSeed, len(p.Stages))
	for i, st := range p.Stages {
		open[i] = deals.StageSeed{Name: st.Name, WinProbability: st.Probability}
	}
	return dealsH.SeedWorkspacePipelineTx(ctx, tx, p.Name, open)
}

func seedConsent(ctx context.Context, tx pgx.Tx, configured []deployconfig.ConsentPurpose) error {
	if len(configured) == 0 {
		if err := consent.SeedDefaultPurposesTx(ctx, tx); err != nil {
			return err
		}
		return consent.SeedDefaultRetentionTx(ctx, tx)
	}
	purposes := make([]consent.PurposeSeed, len(configured))
	for i, p := range configured {
		purposes[i] = consent.PurposeSeed{Key: p.Key, Label: p.Label, DoubleOptIn: p.DoubleOptIn}
	}
	if err := consent.SeedPurposesTx(ctx, tx, purposes); err != nil {
		return err
	}
	return consent.SeedDefaultRetentionTx(ctx, tx)
}

// seedBookingPage provisions the admin's public booking page: the
// workspace's only user at seed time IS the bootstrap admin (RLS scopes
// the read).
func seedBookingPage(ctx context.Context, tx pgx.Tx) error {
	var adminID ids.UserID
	if err := tx.QueryRow(ctx,
		`SELECT id FROM app_user WHERE workspace_id = $1 ORDER BY created_at LIMIT 1`,
		storekit.MustWorkspace(ctx)).Scan(&adminID); err != nil {
		return err
	}
	_, err := activities.SeedBookingPageTx(ctx, tx, adminID)
	return err
}
