package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gradionhq/margince/backend/internal/modules/identity/internal/password"
	"github.com/gradionhq/margince/backend/internal/modules/identity/internal/policy"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// Session lifetimes (ADR-0043: idle + absolute, both enforced at lookup).
// Rolling idle window capped by the absolute expiry; both are documented
// operational defaults, not spec-ratified numbers.
const (
	idleTTL     = 24 * time.Hour
	absoluteTTL = 30 * 24 * time.Hour
)

// Service owns identity: workspaces, users, opaque server-side sessions.
type Service struct {
	pool *pgxpool.Pool
}

func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

// Identity is the authenticated principal's resolved state — what /me
// returns and what the middleware binds into the request context.
type Identity struct {
	UserID      ids.UUID
	WorkspaceID ids.UUID
	Email       string
	DisplayName string
	SeatType    string
	Roles       []string
	Teams       []ids.UUID
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

// Bootstrap creates workspace + admin + seeded roles + other modules'
// per-workspace defaults in ONE transaction, and opens the admin's first
// session. The workspace insert runs before the GUC exists; everything
// tenant-scoped happens after set_config binds the fresh workspace id, so
// RLS holds even during bootstrap. seed runs inside this transaction with
// a system actor + the workspace GUC bound — a seed failure rolls the
// whole tenant back, so no half-provisioned workspace can survive (C5).
// seed may be nil (no cross-module defaults to lay down).
func (s *Service) Bootstrap(ctx context.Context, in BootstrapInput, seed func(ctx context.Context, tx pgx.Tx) error) (Identity, string, error) {
	if in.Timezone == "" {
		in.Timezone = "UTC"
	}
	hash, err := password.Hash(in.AdminPassword)
	if err != nil {
		return Identity{}, "", err
	}
	token, tokenHash, err := mintSessionToken()
	if err != nil {
		return Identity{}, "", err
	}

	var id Identity
	err = database.WithInfraTx(ctx, s.pool, func(tx pgx.Tx) error {
		var wsID ids.UUID
		err := tx.QueryRow(ctx,
			`INSERT INTO workspace (name, slug, base_currency, timezone) VALUES ($1, $2, 'EUR', $3) RETURNING id`,
			in.WorkspaceName, in.Slug, in.Timezone).Scan(&wsID)
		if err != nil {
			if isUniqueViolation(err) {
				return &slugTakenError{slug: in.Slug}
			}
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT set_config('app.workspace_id', $1, true)`, wsID.String()); err != nil {
			return err
		}

		var userID ids.UUID
		err = tx.QueryRow(ctx,
			`INSERT INTO app_user (workspace_id, email, password_hash, display_name, timezone)
			 VALUES ($1, lower($2), $3, $4, $5) RETURNING id`,
			wsID, in.AdminEmail, hash, in.AdminName, in.Timezone).Scan(&userID)
		if err != nil {
			return err
		}

		var adminRoleID ids.UUID
		for _, role := range systemRoles {
			var roleID ids.UUID
			err := tx.QueryRow(ctx,
				`INSERT INTO role (workspace_id, key, name, is_system, permissions) VALUES ($1, $2, $3, true, $4) RETURNING id`,
				wsID, role.key, role.name, policy.DefaultJSON(role.key)).Scan(&roleID)
			if err != nil {
				return err
			}
			if role.key == "admin" {
				adminRoleID = roleID
			}
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO role_assignment (workspace_id, role_id, user_id) VALUES ($1, $2, $3)`,
			wsID, adminRoleID, userID); err != nil {
			return err
		}

		if err := insertSession(ctx, tx, wsID, userID, tokenHash); err != nil {
			return err
		}
		if err := auditLogin(ctx, tx, wsID, userID, "workspace bootstrap"); err != nil {
			return err
		}

		adminDoc, err := policy.Parse(policy.DefaultJSON("admin"))
		if err != nil {
			return err
		}
		id = Identity{
			UserID: userID, WorkspaceID: wsID,
			Email: in.AdminEmail, DisplayName: in.AdminName,
			SeatType: "full", Roles: []string{"admin"},
			Permissions: policy.Merge(map[string]policy.Document{"admin": adminDoc}),
		}

		// Lay down other modules' per-workspace defaults in THIS
		// transaction, as the system actor, so the whole tenant — identity
		// and defaults — is atomic (C5). The GUC is already bound above.
		if seed != nil {
			seedCtx := principal.WithActor(principal.WithWorkspaceID(ctx, wsID), principal.Principal{
				Type: principal.PrincipalSystem, ID: "system",
			})
			if err := seed(seedCtx, tx); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return Identity{}, "", err
	}
	return id, token, nil
}

// slugTakenError maps to 409 in the handler.
type slugTakenError struct{ slug string }

func (e *slugTakenError) Error() string { return fmt.Sprintf("workspace slug %q taken", e.slug) }
func (e *slugTakenError) Is(target error) bool {
	return target == apperrors.ErrConflict
}

// ResolveWorkspace maps a tenant slug (subdomain in production, header in
// dev) to its id. Pre-auth by necessity: the workspace table is the one
// non-tenant table.
func (s *Service) ResolveWorkspace(ctx context.Context, slug string) (ids.UUID, error) {
	var id ids.UUID
	err := database.WithInfraTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT id FROM workspace WHERE slug = $1 AND archived_at IS NULL`, slug).Scan(&id)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ids.Nil, apperrors.ErrNotFound
	}
	return id, err
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
		panic(fmt.Sprintf("crmauth: crypto/rand unavailable: %v", err))
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

// Login verifies credentials inside the tenant transaction and mints an
// opaque session. Every attempt outcome — success or failure — lands in
// audit_log (the failure row commits in its own transaction, because the
// attempt's transaction rolls back with the error).
func (s *Service) Login(ctx context.Context, email, plaintext string) (Identity, string, error) {
	wsID, ok := principal.WorkspaceID(ctx)
	if !ok {
		return Identity{}, "", database.ErrNoWorkspace
	}
	token, tokenHash, err := mintSessionToken()
	if err != nil {
		return Identity{}, "", err
	}

	var id Identity
	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var userID ids.UUID
		var hash *string
		var displayName, seatType string
		err := tx.QueryRow(ctx,
			`SELECT id, password_hash, display_name, seat_type FROM app_user
			 WHERE lower(email) = lower($1) AND status = 'active' AND archived_at IS NULL`,
			email).Scan(&userID, &hash, &displayName, &seatType)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && hash == nil) {
			_ = password.Verify(plaintext, decoyHash) // equal work on both paths
			return ErrBadCredentials
		}
		if err != nil {
			return err
		}
		if err := password.Verify(plaintext, *hash); err != nil {
			if errors.Is(err, password.ErrMismatch) {
				return ErrBadCredentials
			}
			return err
		}

		if err := insertSession(ctx, tx, wsID, userID, tokenHash); err != nil {
			return err
		}
		if err := auditLogin(ctx, tx, wsID, userID, "password login"); err != nil {
			return err
		}

		id = Identity{UserID: userID, WorkspaceID: wsID, Email: email, DisplayName: displayName, SeatType: seatType}
		var loadErr error
		id.Roles, id.Teams, id.Permissions, loadErr = loadGrants(ctx, tx, userID)
		return loadErr
	})
	if errors.Is(err, ErrBadCredentials) {
		// The attempt's transaction rolled back with the error, so the
		// failure audit needs its own transaction — an invisible
		// brute-force is exactly what the audit trail exists to catch.
		// A failure writing it outranks the 401.
		if auditErr := s.auditFailedLogin(ctx, wsID, email); auditErr != nil {
			return Identity{}, "", auditErr
		}
		return Identity{}, "", err
	}
	if err != nil {
		return Identity{}, "", err
	}
	return id, token, nil
}

// auditFailedLogin records one failed password attempt. The attempted
// email rides evidence (there may be no user row to reference); the
// actor is the anonymous claimant, not a resolved identity.
func (s *Service) auditFailedLogin(ctx context.Context, wsID ids.UUID, email string) error {
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO audit_log (workspace_id, actor_type, actor_id, action, entity_type, evidence)
			 VALUES ($1, 'human', 'human:unauthenticated', 'login', 'session',
			         jsonb_build_object('outcome', 'failed', 'email', $2::text))`,
			wsID, email)
		return err
	})
}

// Authenticate resolves a session cookie value to its Identity, enforcing
// revocation + idle + absolute expiry at lookup, and rolls the idle window
// forward.
func (s *Service) Authenticate(ctx context.Context, rawToken string) (Identity, error) {
	tokenHash := hashToken(rawToken)

	var id Identity
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var sessionID, userID ids.UUID
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
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE session SET revoked_at = now() WHERE token_hash = $1 AND revoked_at IS NULL`,
			hashToken(rawToken))
		return err
	})
}

func insertSession(ctx context.Context, tx pgx.Tx, wsID, userID ids.UUID, tokenHash string) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO session (workspace_id, user_id, token_hash, idle_expires_at, expires_at)
		 VALUES ($1, $2, $3, now() + $4::interval, now() + $5::interval)`,
		wsID, userID, tokenHash, idleTTL.String(), absoluteTTL.String())
	return err
}

func auditLogin(ctx context.Context, tx pgx.Tx, wsID, userID ids.UUID, detail string) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO audit_log (workspace_id, actor_type, actor_id, action, entity_type, evidence)
		 VALUES ($1, 'human', $2, 'login', 'session', jsonb_build_object('detail', $3::text))`,
		wsID, "human:"+userID.String(), detail)
	return err
}

func loadGrants(ctx context.Context, tx pgx.Tx, userID ids.UUID) (roles []string, teams []ids.UUID, perms principal.Permissions, err error) {
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
		var t ids.UUID
		if err := teamRows.Scan(&t); err != nil {
			return nil, nil, principal.Permissions{}, err
		}
		teams = append(teams, t)
	}
	return roles, teams, policy.Merge(byRole), teamRows.Err()
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

func isUniqueViolation(err error) bool {
	var pgErr interface{ SQLState() string }
	return errors.As(err, &pgErr) && pgErr.SQLState() == "23505"
}
