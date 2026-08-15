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
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
	"github.com/gradionhq/margince/backend/internal/modules/automation"
	"github.com/gradionhq/margince/backend/internal/modules/consent"
	"github.com/gradionhq/margince/backend/internal/modules/deals"
	"github.com/gradionhq/margince/backend/internal/modules/identity"
	"github.com/gradionhq/margince/backend/internal/modules/privacy"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
	"github.com/gradionhq/margince/backend/internal/platform/settings"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// EnsureInstallation applies the boot state machine before the API
// serves: 0 active workspaces → bootstrap organization + first admin +
// seeds atomically from cfg (requires organization + bootstrap_admin);
// 1 → bind; >1 → refuse with the operator-facing invariant error.
// Restarts are idempotent — bootstrap values never reconcile into an
// existing organization.
func EnsureInstallation(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger, cfg deployconfig.Config) error {
	var create func() (identity.InstallationBootstrap, error)
	if b := cfg.BootstrapAdmin; b != nil {
		if cfg.Organization.Name == "" {
			return errors.New("compose: bootstrap_admin is configured but organization.name is missing — both are required to bootstrap an empty database")
		}
		// The password secret is read inside this closure, which bootstrap
		// calls only when it is actually creating the organization. Reading it
		// here would read it on every boot, and ADR-0061 §2 permits deleting
		// the secret once the organization exists — so an installation that
		// followed the ADR would stop booting.
		create = func() (identity.InstallationBootstrap, error) {
			pw, err := b.Password()
			if err != nil {
				return identity.InstallationBootstrap{}, err
			}
			return identity.InstallationBootstrap{
				OrganizationName: cfg.Organization.Name,
				BaseCurrency:     cfg.Organization.BaseCurrency,
				Timezone:         cfg.Organization.Timezone,
				AdminEmail:       b.Email,
				AdminName:        b.DisplayName,
				AdminPassword:    pw,
			}, nil
		}
	}

	svc := identity.NewService(pool)
	wsID, created, err := svc.BootstrapInstallation(ctx, create, configuredSeed(cfg.Seeds, deals.NewHandlers(InstallationDB(pool), DealsInstallation())))
	if errors.Is(err, identity.ErrNotBootstrapped) {
		// An empty database and no configured bootstrap_admin is not an error:
		// it is the claim path (ADR-0105). Boot mints the token the first human
		// presents and CONTINUES — the api has to serve for the claim to be
		// possible at all, and every tenant route answers 503 until it happens.
		return announceSetupToken(ctx, svc, log)
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

// setupTokenFile is where the plaintext claim credential is left for the
// operator, relative to the process working directory. It joins the config/
// directory the installation's other local secrets already live in
// (margince-admin-password, margince-license), which is the directory
// .gitignore anchors — a credential written anywhere else lands untracked in a
// working tree that is a public repository.
const setupTokenFile = "config/margince-setup-token" // #nosec G101 -- a path, not a credential; the token itself is never a literal

// announceSetupToken mints the claim credential when none is outstanding and
// puts it where the operator will find it: the server log, and a 0600 file
// whose resolved path the log names.
//
// Both, not one. The log is where an operator watching a first boot is already
// looking; the file is what survives a log pipeline that dropped the line, and
// what a `kubectl exec` can read afterwards. Neither is the database — only the
// hash is stored there, so a backup cannot be replayed into a claim.
//
// An already-outstanding token is reported, never replaced: a boot that minted
// a fresh one would silently invalidate the token an operator had already read
// and handed on.
func announceSetupToken(ctx context.Context, svc *identity.Service, log *slog.Logger) error {
	raw, err := svc.MintSetupToken(ctx)
	if errors.Is(err, identity.ErrSetupTokenExists) {
		abs, pathErr := filepath.Abs(setupTokenFile)
		if pathErr != nil {
			abs = setupTokenFile
		}
		log.Warn("installation is unprovisioned and a setup token is already outstanding — the one issued earlier is still the credential; if it was lost, replace it with `margince-migrate setup-token`",
			"token_file", abs)
		return nil
	}
	if err != nil {
		return fmt.Errorf("compose: minting the setup token for an unprovisioned installation: %w", err)
	}
	path, writeErr := writeSetupTokenFile(raw)
	// The log line carries the token itself, so it survives a failed file
	// write — that is the whole reason for having two channels rather than
	// one, and refusing to boot here would strand an installation over a
	// read-only directory when the operator can already read the token above.
	log.Warn("installation is unprovisioned: claim it with this one-time setup token",
		"setup_token", raw, "token_file", path, "write_error", writeErr)
	return nil
}

// writeSetupTokenFile writes the plaintext 0600 and returns the path it used,
// reporting rather than returning a write failure — see announceSetupToken.
//
// O_EXCL|O_NOFOLLOW rather than os.WriteFile, because what is written here is a
// credential that claims the installation. WriteFile is O_CREATE|O_TRUNC: it
// follows a symlink and leaves an existing file's mode alone, so anything that
// can write this directory before first boot — a group-writable /app, a shared
// volume, a sidecar — can pre-create the path 0666 and read the credential, or
// point it at a file the server user may overwrite. Refusing an existing entry
// is the honest outcome: the operator sees the write error next to the token
// itself in the log.
func writeSetupTokenFile(raw string) (path string, err error) {
	abs, err := filepath.Abs(setupTokenFile)
	if err != nil {
		return setupTokenFile, err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return abs, err
	}
	// #nosec G304 -- abs derives from the compile-time constant above via filepath.Abs; no request or configuration value reaches this path
	f, err := os.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return abs, err
	}
	if _, err := f.WriteString(raw); err != nil {
		return abs, errors.Join(err, f.Close())
	}
	return abs, f.Close()
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
		if err := seedRetentionPosture(ctx, tx, seeds); err != nil {
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

// seedRetentionPosture turns the retain-only posture on when the deployment asked
// for it (GCS-PARAM-7). It runs INSIDE the bootstrap transaction, beside the
// policy rows it governs: an installation that declared it destroys nothing must
// not be reachable in a state where the rows exist and the posture does not, even
// briefly.
//
// The standard posture writes NO row, deliberately. An absent setting reads as
// its registered default (false), so seeding a row that merely restates the
// default would add a row saying nothing and make "has anyone ever changed this?"
// unanswerable — the same reasoning 0190 applied to capture_auto_enrich.
func seedRetentionPosture(ctx context.Context, tx pgx.Tx, seeds deployconfig.Seeds) error {
	if !seeds.RetainOnly() {
		return nil
	}
	return settings.SeedValue(ctx, tx, privacy.RetainOnly, true)
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
