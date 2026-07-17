// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// One installation serves one organization (A107/ADR-0061). The workspace
// row remains the internal singleton boundary: this file owns its
// boot-time creation from deployment configuration and its resolution for
// every request. The invariant is enforced here, at boot and at lookup —
// deliberately NOT as a schema constraint, so cross-tenant RLS tests keep
// proving isolation by inserting a second workspace directly.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/modules/identity/internal/password"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// ErrNotBootstrapped means the database holds no active organization.
// The API refuses to serve (it bootstraps at boot or dies); the worker
// retries until the API has bootstrapped; the MCP binary exits with this
// as an operator error — pre-bootstrap no human exists who could have
// granted a passport.
var ErrNotBootstrapped = errors.New("identity: installation not bootstrapped — no active organization exists")

// ErrMultipleWorkspaces means the database violates the
// single-organization invariant. Never auto-resolved — an operator
// explicitly retains one organization and archives the rest (ADR-0061 §3).
var ErrMultipleWorkspaces = errors.New("identity: more than one active workspace — the single-organization invariant requires an operator-led migration")

// installationLockKey serializes bootstrap across concurrently starting
// processes (pg_advisory_xact_lock). The value is arbitrary but fixed —
// every binary of this installation must agree on it.
const installationLockKey = int64(0x4d61726761_0001) // "Marga"+1

// InstallationBootstrap is the boot-time creation input, sourced from the
// deployment configuration file — never from a request body.
type InstallationBootstrap struct {
	OrganizationName string
	BaseCurrency     string
	Timezone         string
	AdminEmail       string
	AdminName        string
	AdminPassword    string
}

// BootstrapInstallation binds the installation to its singleton
// organization, creating it when the database is empty. Under a
// transaction-scoped advisory lock (so concurrent API starts cannot race
// a second organization into existence) it applies the ADR-0061 state
// machine: 0 active workspaces → create organization + first admin +
// system roles + seeds atomically; 1 → bind to it; >1 → refuse.
//
// create is nil when no bootstrap_admin is configured — then an empty
// database is ErrNotBootstrapped instead of being claimable. No session
// is minted: the first admin signs in through the normal login, and
// bootstrap values never reconcile into an existing organization
// (restart never resets a password, role, or seed).
func (s *Service) BootstrapInstallation(ctx context.Context, create *InstallationBootstrap, seed func(ctx context.Context, tx pgx.Tx) error) (wsID ids.WorkspaceID, created bool, err error) {
	err = database.WithInfraTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, installationLockKey); err != nil {
			return fmt.Errorf("identity: taking the bootstrap advisory lock: %w", err)
		}
		existing, err := activeWorkspaces(ctx, tx)
		if err != nil {
			return err
		}
		switch {
		case len(existing) == 1:
			wsID = existing[0]
			return nil
		case len(existing) > 1:
			return ErrMultipleWorkspaces
		case create == nil:
			return ErrNotBootstrapped
		}
		wsID, err = createInstallation(ctx, tx, *create, seed)
		created = err == nil
		return err
	})
	if err != nil {
		return ids.WorkspaceID{}, false, err
	}
	s.installation.Store(&wsID)
	return wsID, created, nil
}

// InstallationWorkspace resolves the singleton organization for a
// request, cached after the first successful lookup — the resolution the
// per-request slug used to provide. Pre-bootstrap lookups return
// ErrNotBootstrapped (never cached: the worker polls this until the API
// bootstraps, then binds).
func (s *Service) InstallationWorkspace(ctx context.Context) (ids.WorkspaceID, error) {
	if cached := s.installation.Load(); cached != nil {
		return *cached, nil
	}
	var wsID ids.WorkspaceID
	err := database.WithInfraTx(ctx, s.pool, func(tx pgx.Tx) error {
		existing, err := activeWorkspaces(ctx, tx)
		if err != nil {
			return err
		}
		switch len(existing) {
		case 0:
			return ErrNotBootstrapped
		case 1:
			wsID = existing[0]
			return nil
		default:
			return ErrMultipleWorkspaces
		}
	})
	if err != nil {
		return ids.WorkspaceID{}, err
	}
	s.installation.Store(&wsID)
	return wsID, nil
}

// activeWorkspaces lists un-archived workspace ids. LIMIT 3: the caller
// only distinguishes zero, one, and too-many.
func activeWorkspaces(ctx context.Context, tx pgx.Tx) ([]ids.WorkspaceID, error) {
	rows, err := tx.Query(ctx, `SELECT id FROM workspace WHERE archived_at IS NULL LIMIT 3`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ids.WorkspaceID
	for rows.Next() {
		var id ids.WorkspaceID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// createInstallation writes organization + first admin + system roles +
// module seeds in the caller's transaction — either everything exists
// afterwards or nothing does (the ADR-0043 bootstrap atomicity, kept).
func createInstallation(ctx context.Context, tx pgx.Tx, in InstallationBootstrap, seed func(ctx context.Context, tx pgx.Tx) error) (ids.WorkspaceID, error) {
	boot := BootstrapInput{
		WorkspaceName: in.OrganizationName,
		Slug:          slugify(in.OrganizationName),
		AdminEmail:    in.AdminEmail,
		AdminName:     in.AdminName,
		AdminPassword: in.AdminPassword,
		Timezone:      in.Timezone,
	}
	if err := boot.normalize(); err != nil {
		return ids.WorkspaceID{}, err
	}
	currency := in.BaseCurrency
	if currency == "" {
		currency = "EUR"
	}
	hash, err := password.Hash(boot.AdminPassword)
	if err != nil {
		return ids.WorkspaceID{}, err
	}

	var wsID ids.WorkspaceID
	if err := tx.QueryRow(ctx,
		`INSERT INTO workspace (name, slug, base_currency, timezone) VALUES ($1, $2, $3, $4) RETURNING id`,
		boot.WorkspaceName, boot.Slug, currency, boot.Timezone).Scan(&wsID); err != nil {
		return ids.WorkspaceID{}, err
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.workspace_id', $1, true)`, wsID.String()); err != nil {
		return ids.WorkspaceID{}, err
	}

	var userID ids.UserID
	if err := tx.QueryRow(ctx,
		`INSERT INTO app_user (workspace_id, email, password_hash, display_name, timezone)
		 VALUES ($1, lower($2), $3, $4, $5) RETURNING id`,
		wsID, boot.AdminEmail, hash, boot.AdminName, boot.Timezone).Scan(&userID); err != nil {
		return ids.WorkspaceID{}, err
	}
	if err := seedSystemRoles(ctx, tx, wsID, userID); err != nil {
		return ids.WorkspaceID{}, err
	}
	// Bootstrap is a SYSTEM event: no human signed in — the admin's
	// first session is minted later by a normal login with its own row.
	if _, err := tx.Exec(ctx,
		`INSERT INTO system_log (workspace_id, actor_type, actor_id, action, detail)
		 VALUES ($1, 'system', 'installation-bootstrap', 'installation_bootstrap', jsonb_build_object('admin_user_id', $2::text))`,
		wsID, userID.String()); err != nil {
		return ids.WorkspaceID{}, err
	}

	if seed != nil {
		// Boot bootstrap IS the originating operation: it mints the one
		// correlation id its seed writes (pipeline.created, …) trace to —
		// the id the HTTP middleware would have minted per request.
		seedCtx := principal.WithActor(principal.WithWorkspaceID(ctx, wsID.UUID), principal.Principal{
			Type: principal.PrincipalSystem, ID: "system",
		})
		seedCtx = principal.WithCorrelationID(seedCtx, ids.NewV7())
		if err := seed(seedCtx, tx); err != nil {
			return ids.WorkspaceID{}, err
		}
	}
	return wsID, nil
}

// slugify derives the workspace's stable slug from the organization name
// — an internal identifier now (no subdomain resolves it), kept
// subdomain-safe for the schema's slug shape.
func slugify(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
