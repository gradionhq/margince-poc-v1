// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/identity/internal/password"
	"github.com/gradionhq/margince/backend/internal/modules/identity/internal/policy"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
)

// Session lifetimes (ADR-0043: idle + absolute, both enforced at lookup).
// Rolling idle window capped by the absolute expiry; both are documented
// operational defaults, not spec-ratified numbers.
const (
	idleTTL     = 24 * time.Hour
	absoluteTTL = 30 * 24 * time.Hour
)

// Service owns identity: the singleton organization, users, opaque
// server-side sessions.
type Service struct {
	pool *pgxpool.Pool
	// now is the service's clock: the §27 lockout window and duration are
	// judged against it, so tests prove the lock/expiry transitions
	// without sleeping. Session/passport expiries stay on the database's
	// now() — they are enforced inside SQL predicates, not Go logic.
	now func() time.Time
	// installation caches the singleton workspace id after the first
	// successful resolution (installation.go) — the id is immutable for
	// the process lifetime, so no request pays the lookup twice.
	installation atomic.Pointer[ids.WorkspaceID]
}

func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool, now: time.Now}
}

// Identity is the authenticated principal's resolved state — what /me
// returns and what the middleware binds into the request context.
type Identity struct {
	UserID      ids.UserID
	WorkspaceID ids.WorkspaceID
	Email       string
	DisplayName string
	SeatType    string
	Roles       []string
	Teams       []ids.TeamID
	Permissions principal.Permissions
}

// systemRoles is the seeded default role set (data-model §2.4); custom
// roles beyond these are a code extension, not a runtime builder.
var systemRoles = []struct{ key, name string }{
	{"admin", "Admin"},
	{"manager", "Manager"},
	{"rep", "Rep"},
	{"read_only", "Read-only"},
	{"ops", "Ops / Integrations"},
}

// BootstrapInput creates the tenant root and its first admin.
type BootstrapInput struct {
	WorkspaceName string
	Slug          string
	AdminEmail    string
	AdminName     string
	AdminPassword string
	Timezone      string
}

// normalize parse-don't-validates the tenant-root inputs in place. The slug
// becomes the workspace's subdomain and the timezone drives every
// date-boundary sweep — a malformed value here would haunt the whole tenant
// lifetime, so it is rejected before any row is written.
func (in *BootstrapInput) normalize() error {
	if in.Timezone == "" {
		in.Timezone = "UTC"
	}
	slug, err := values.ParseSlug(in.Slug)
	if err != nil {
		return err
	}
	in.Slug = slug.String()
	tz, err := values.ParseTimezone(in.Timezone)
	if err != nil {
		return err
	}
	in.Timezone = tz.String()
	adminEmail, err := values.ParseEmail(in.AdminEmail)
	if err != nil {
		return err
	}
	in.AdminEmail = adminEmail.String()
	return nil
}

// seedSystemRoles lays down the compiled-in role set for a fresh
// workspace and assigns the admin role to its first user — part of the
// Bootstrap transaction, so a partial role set can never survive.
func seedSystemRoles(ctx context.Context, tx pgx.Tx, wsID ids.WorkspaceID, adminUserID ids.UserID) error {
	// note: role is not a first-class entity in the id kind vocabulary, so
	// its ids stay ids.UUID (kernel gap — no RoleKind to assert).
	var adminRoleID ids.UUID
	for _, role := range systemRoles {
		var roleID ids.UUID
		err := tx.QueryRow(ctx,
			`INSERT INTO role (workspace_id, key, name, is_system, permissions) VALUES ($1, $2, $3, true, $4) RETURNING id`,
			wsID, role.key, role.name, policy.MustDefaultJSON(role.key)).Scan(&roleID)
		if err != nil {
			return err
		}
		if role.key == "admin" {
			adminRoleID = roleID
		}
	}
	_, err := tx.Exec(ctx,
		`INSERT INTO role_assignment (workspace_id, role_id, user_id) VALUES ($1, $2, $3)`,
		wsID, adminRoleID, adminUserID)
	return err
}

// ErrBadCredentials deliberately does not distinguish unknown-user from
// wrong-password.
var ErrBadCredentials = errors.New("crmauth: invalid email or password")

// decoyHash is verified against on the unknown-user / no-password branch
// so a failed login costs the full Argon2 work either way — without it,
// the latency difference discloses which emails exist even though the
// response body does not. Minted once at startup from a
// throwaway random secret nobody knows.
var decoyHash = func() string {
	h, err := password.Hash(mustRandomSecret())
	if err != nil {
		panic(fmt.Sprintf("crmauth: minting decoy hash: %v", err))
	}
	return h
}()

func mustRandomSecret() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		//craft:ignore panic-in-domain runs only during package initialization (the decoyHash var) — a process without crypto/rand cannot mint any credential and must not boot
		panic(fmt.Sprintf("crmauth: crypto/rand unavailable: %v", err))
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

// Login verifies credentials inside the tenant transaction and mints an
// opaque session. Every attempt outcome — success or failure — lands in
// audit_log (the failure row commits in its own transaction, because the
// attempt's transaction rolls back with the error).
func (s *Service) Login(ctx context.Context, email, plaintext string) (Identity, string, error) {
	rawWsID, ok := principal.WorkspaceID(ctx)
	if !ok {
		// The middleware binds the singleton organization on every request
		// (installation.go); an unbound context means the installation is
		// not bootstrapped — and the answer must not disclose that:
		// credentials against a not-yet-existing organization read exactly
		// like wrong credentials.
		return Identity{}, "", ErrBadCredentials
	}
	wsID := ids.From[ids.WorkspaceKind](rawWsID)
	token, tokenHash, err := mintSessionToken()
	if err != nil {
		return Identity{}, "", err
	}

	var id Identity
	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		account, err := s.checkCredentials(ctx, tx, email, plaintext)
		if err != nil {
			return err
		}
		if err := insertSession(ctx, tx, wsID, account.UserID, tokenHash); err != nil {
			return err
		}
		if err := auditLogin(ctx, tx, wsID, account.UserID, "password login"); err != nil {
			return err
		}

		id = Identity{UserID: account.UserID, WorkspaceID: wsID, Email: email, DisplayName: account.DisplayName, SeatType: account.SeatType}
		var loadErr error
		id.Roles, id.Teams, id.Permissions, loadErr = loadGrants(ctx, tx, account.UserID)
		return loadErr
	})
	if errors.Is(err, ErrBadCredentials) {
		// The attempt's transaction rolled back with the error, so the
		// failure audit needs its own transaction — an invisible
		// brute-force is exactly what the audit trail exists to catch.
		// A failure writing it outranks the 401.
		if auditErr := s.recordFailedLogin(ctx, wsID, email); auditErr != nil {
			return Identity{}, "", auditErr
		}
		return Identity{}, "", err
	}
	if err != nil {
		return Identity{}, "", err
	}
	return id, token, nil
}

// Authenticate resolves a session cookie value to its Identity, enforcing
// revocation + idle + absolute expiry at lookup, and rolls the idle window
// forward.
func (s *Service) Authenticate(ctx context.Context, rawToken string) (Identity, error) {
	tokenHash := hashToken(rawToken)

	var id Identity
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		// note: a session is keyed by its opaque token, not exposed as a
		// first-class entity id — its row id has no kind and stays ids.UUID.
		var sessionID ids.UUID
		var userID ids.UserID
		err := tx.QueryRow(ctx,
			`SELECT s.id, u.id, u.email, u.display_name, u.seat_type, s.workspace_id
			 FROM session s
			 JOIN app_user u ON u.id = s.user_id
			 WHERE s.token_hash = $1
			   AND s.revoked_at IS NULL
			   AND now() < s.idle_expires_at
			   AND now() < s.expires_at
			   AND u.status = 'active' AND u.archived_at IS NULL`,
			tokenHash).Scan(&sessionID, &userID, &id.Email, &id.DisplayName, &id.SeatType, &id.WorkspaceID)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		if err != nil {
			return err
		}
		id.UserID = userID

		if _, err := tx.Exec(ctx,
			`UPDATE session SET last_seen_at = now(),
			   idle_expires_at = least(now() + $2::interval, expires_at)
			 WHERE id = $1`, sessionID, idleTTL.String()); err != nil {
			return err
		}
		var loadErr error
		id.Roles, id.Teams, id.Permissions, loadErr = loadGrants(ctx, tx, userID)
		return loadErr
	})
	if err != nil {
		return Identity{}, err
	}
	return id, nil
}

// Logout revokes the session behind the cookie. Revoking an unknown or
// already-revoked token is a no-op: logout is idempotent.
func (s *Service) Logout(ctx context.Context, rawToken string) error {
	if _, ok := principal.WorkspaceID(ctx); !ok {
		// A workspace that doesn't resolve holds no sessions — nothing to
		// revoke, same no-op as an unknown token.
		return nil
	}
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE session SET revoked_at = now() WHERE token_hash = $1 AND revoked_at IS NULL`,
			hashToken(rawToken))
		return err
	})
}

func insertSession(ctx context.Context, tx pgx.Tx, wsID ids.WorkspaceID, userID ids.UserID, tokenHash string) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO session (workspace_id, user_id, token_hash, idle_expires_at, expires_at)
		 VALUES ($1, $2, $3, now() + $4::interval, now() + $5::interval)`,
		wsID, userID, tokenHash, idleTTL.String(), absoluteTTL.String())
	return err
}

// auditLogin appends the login fact to system_log — the ledger for
// non-entity operational events. A login mutates no record (it has no
// entity), so it belongs in system_log, not the audit_log record-mutation
// spine. It writes the row directly (not via storekit.LogSystem) because
// the login path has no authenticated principal for LogSystem to stamp from
// — the same reason identity owns its own audit-ledger writer.
func auditLogin(ctx context.Context, tx pgx.Tx, wsID ids.WorkspaceID, userID ids.UserID, detail string) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO system_log (workspace_id, actor_type, actor_id, action, detail)
		 VALUES ($1, 'human', $2, 'login', jsonb_build_object('detail', $3::text))`,
		wsID, "human:"+userID.String(), detail)
	return err
}

func loadGrants(ctx context.Context, tx pgx.Tx, userID ids.UserID) (roles []string, teams []ids.TeamID, perms principal.Permissions, err error) {
	rows, err := tx.Query(ctx,
		`SELECT r.key, r.permissions FROM role_assignment ra JOIN role r ON r.id = ra.role_id WHERE ra.user_id = $1`, userID)
	if err != nil {
		return nil, nil, principal.Permissions{}, err
	}
	defer rows.Close()
	byRole := map[string]policy.Document{}
	for rows.Next() {
		var key string
		var raw []byte
		if err := rows.Scan(&key, &raw); err != nil {
			return nil, nil, principal.Permissions{}, err
		}
		doc, err := policy.Parse(raw)
		if err != nil {
			// A role carrying an invalid policy document is a data defect
			// the login must surface, not silently downgrade to no access.
			return nil, nil, principal.Permissions{}, fmt.Errorf("crmauth: role %q: %w", key, err)
		}
		roles = append(roles, key)
		byRole[key] = doc
	}
	if err := rows.Err(); err != nil {
		return nil, nil, principal.Permissions{}, err
	}

	teamRows, err := tx.Query(ctx,
		`SELECT team_id FROM team_membership WHERE user_id = $1`, userID)
	if err != nil {
		return nil, nil, principal.Permissions{}, err
	}
	defer teamRows.Close()
	for teamRows.Next() {
		var t ids.TeamID
		if err := teamRows.Scan(&t); err != nil {
			return nil, nil, principal.Permissions{}, err
		}
		teams = append(teams, t)
	}
	return roles, teams, policy.Merge(byRole), teamRows.Err()
}

// rawTeamIDs widens typed team ids to the untyped []ids.UUID the kernel
// principal and the authz port carry — the row-scope seams stay untyped
// (they compare team membership against polymorphic scope clauses).
func rawTeamIDs(teams []ids.TeamID) []ids.UUID {
	if teams == nil {
		return nil
	}
	out := make([]ids.UUID, len(teams))
	for i, t := range teams {
		out[i] = t.UUID
	}
	return out
}

// mintSessionToken returns the raw cookie value and the SHA-256 hex the
// database stores — the raw token never touches the DB (ADR-0043).
func mintSessionToken() (raw, hash string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("crmauth: minting session token: %w", err)
	}
	raw = base64.RawURLEncoding.EncodeToString(buf)
	return raw, hashToken(raw), nil
}

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
