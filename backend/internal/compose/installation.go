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
	"strings"
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
	"github.com/gradionhq/margince/backend/internal/platform/config"
	"github.com/gradionhq/margince/backend/internal/platform/deployconfig"
	"github.com/gradionhq/margince/backend/internal/platform/ownedfile"
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
			pw, err := b.ResolvePassword(config.FromOS)
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
	wsID, created, discarded, err := svc.BootstrapInstallation(ctx, create, configuredSeed(cfg.Seeds, deals.NewHandlers(InstallationDB(pool), DealsInstallation())))
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
	// `setting` is not tenant-scoped, so a bootstrap over a database that still
	// holds a previous installation's rows creates a new workspace beside them
	// and keeps the OLD identity. The values in margince.yaml are then read,
	// validated, and dropped. This is the whole of #863 that needs no product
	// ruling: what should happen instead is undecided, but the operator should
	// not have to infer from the UI that their file was ignored.
	if len(discarded) > 0 {
		log.Warn("this installation kept the identity already stored and discarded the values margince.yaml supplied; a previous installation's settings survived because they are not scoped to a workspace",
			"discarded_keys", strings.Join(discarded, ", "), "workspace_id", wsID.String())
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
// puts it where the operator will find it: a 0600 file, whose resolved path the
// server log names.
//
// Two channels, and only one of them carries the value. The file is what a
// `kubectl exec` can read afterwards and what survives a log pipeline that
// dropped the line; the log is where an operator watching a first boot is
// already looking, so it names the path and falls back to the token itself only
// when the file could not be written. Neither is the database — only the hash is
// stored there, so a backup cannot be replayed into a claim.
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
	// The credential reaches the log only when the file could not hold it. The
	// two channels are not interchangeable: the 0600 file has one reader and a
	// lifetime, while the log is read by everyone the log store admits and keeps
	// the token in a searchable index long after the installation is claimed. A
	// write failure must not strand an installation over a read-only directory,
	// so the boot continues either way — it is the token, not the announcement,
	// that the successful write withholds.
	if writeErr != nil {
		log.Warn("installation is unprovisioned and the setup-token file could not be written: claim it with this one-time setup token",
			"setup_token", raw, "token_file", path, "write_error", writeErr)
		return nil
	}
	log.Warn("installation is unprovisioned: claim it with the one-time setup token in the token file",
		"token_file", path)
	return nil
}

// writeSetupTokenFile writes the plaintext 0600 and returns the path it used,
// reporting rather than returning a write failure — see announceSetupToken.
//
// O_EXCL rather than os.WriteFile, because what is written here is a credential
// that claims the installation. WriteFile is O_CREATE|O_TRUNC: it follows a
// symlink and leaves an existing file's mode alone, so anything that can write
// this directory before first boot — a group-writable /app, a shared volume, a
// sidecar — can pre-create the path 0666 and read the credential, or point it
// at a file the server user may overwrite. Refusing an existing entry is the
// honest outcome: the operator sees the write error next to the token itself in
// the log.
//
// O_EXCL is the flag that does that, everywhere: it refuses ANY existing final
// component, a symlink included. ownedfile.OpenNoFollow is reinforcement whose
// value is per-platform and is documented where it is declared.
//
// The guard covers the final component AND the directory it lands in, and
// neither covers a component above that: a symlink standing in for the working
// directory itself still redirects the whole path. What is defended is the one
// step an attacker who can write the working tree before first boot actually
// takes.
func writeSetupTokenFile(raw string) (path string, err error) {
	abs, err := filepath.Abs(setupTokenFile)
	if err != nil {
		return setupTokenFile, err
	}
	if err := ownTokenDirectory(filepath.Dir(abs)); err != nil {
		return abs, err
	}
	// #nosec G304 -- abs derives from the compile-time constant above via filepath.Abs; no request or configuration value reaches this path
	f, err := os.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_EXCL|ownedfile.OpenNoFollow, 0o600)
	if err != nil {
		return abs, err
	}
	// The mode argument is advisory on Windows, where the ACL decides. A 0600
	// that is not enforced is worse than no file at all, so a directory whose
	// permissions cannot be established refuses the write rather than producing
	// a readable credential — announceSetupToken then hands the operator the
	// token through the log, which is exactly the fallback that arm exists for.
	if err := ownedfile.RestrictToOwner(f); err != nil {
		return abs, errors.Join(err, f.Close(), os.Remove(abs))
	}
	if _, err := f.WriteString(raw); err != nil {
		return abs, errors.Join(err, f.Close())
	}
	return abs, f.Close()
}

// ownTokenDirectory makes the credential's parent directory one this process
// created, or one it has checked is a real directory rather than a redirect.
//
// MkdirAll cannot do this: it follows a symlink standing in for config/ and
// reports success, so the credential is written inside whatever that link names
// — a directory an attacker who got there before first boot owns and can read.
// O_EXCL and O_NOFOLLOW both act on the FINAL component only, so neither sees it.
//
// What this does NOT cover, stated rather than implied: a symlink at any
// component ABOVE the parent (the working directory itself), and a swap of the
// parent between this check and the open below. Closing those needs an openat
// walk from a directory handle, which Windows has no portable equivalent for;
// the case defended here is the one #1579 verified empirically.
func ownTokenDirectory(dir string) error {
	// #nosec G703 -- dir is filepath.Dir of filepath.Abs(setupTokenFile), a compile-time constant; no request or configuration value reaches this path
	info, err := os.Lstat(dir)
	switch {
	case os.IsNotExist(err):
		// Mkdir, not MkdirAll: the parent of config/ is the working directory,
		// which the process is already running in. Creating a chain would mean
		// creating directories nobody asked for on a path nobody verified.
		// #nosec G703 -- same constant-derived path as the Lstat above
		if err := os.Mkdir(dir, 0o700); err != nil {
			return fmt.Errorf("compose: creating the setup-token directory %s: %w", dir, err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("compose: checking the setup-token directory %s: %w", dir, err)
	case info.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf("compose: refusing to write the setup token: %s is a symbolic link, so the credential would be created wherever it points rather than beside the installation's other local secrets", dir)
	case !info.IsDir():
		return fmt.Errorf("compose: refusing to write the setup token: %s exists and is not a directory", dir)
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
	// The stored answer is deliberately dropped here, and this is the one seed
	// where that is defensible: the value is the constant true, so a discard can
	// only have preserved a row that already says what this would have written.
	// Nothing an operator supplied is lost, which is exactly what separates it
	// from the identity trio.
	_, err := settings.SeedValue(ctx, tx, privacy.RetainOnly, true)
	return err
}

// seedBookingPage provisions the admin's public booking page.
//
// The installation holds TWO users at seed time, not one: bootstrap writes the
// admin and the Agent Runner seat in the same transaction, so `now()` gives
// them the same created_at and "first by created_at" decides nothing. The
// is_agent predicate is what names the admin — without it the page a stranger
// books through can be provisioned against the agent seat, and which one it
// lands on is heap order.
func seedBookingPage(ctx context.Context, tx pgx.Tx) error {
	var adminID ids.UserID
	if err := tx.QueryRow(ctx,
		`SELECT id FROM app_user
		  WHERE is_agent = false
		  ORDER BY created_at LIMIT 1`).Scan(&adminID); err != nil {
		return err
	}
	_, err := activities.SeedBookingPageTx(ctx, tx, adminID)
	return err
}
