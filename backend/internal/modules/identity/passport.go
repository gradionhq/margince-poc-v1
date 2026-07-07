// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// Agent Seat Passports (data-model §2.7, ADR-0043): a human binds their
// agent to their OWN identity with a scoped, expiring, revocable bearer
// token. The agent's authority is structurally ≤ the human's — every
// agent call carries the granting human's RBAC and row scope, further
// narrowed by the passport's verb scopes. This is the local/A1 issuance
// path; the hosted A2 surface adds OAuth2 + PKCE + DCR on top (the
// contract gap is recorded in fable feedback/04).

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// passportTokenPrefix makes an agent bearer token visually and
// programmatically distinguishable from a session cookie value, so a
// leaked string is identifiable and the middleware can route it without
// probing both tables.
const passportTokenPrefix = "mgp_"

const (
	defaultPassportTTL = 30 * 24 * time.Hour
	maxPassportTTL     = 90 * 24 * time.Hour
)

// validScopes is the closed verb vocabulary (interfaces.md §2).
var validScopes = map[principal.Scope]bool{
	principal.ScopeRead: true, principal.ScopeDraft: true, principal.ScopeWrite: true,
	principal.ScopeSend: true, principal.ScopeEnrich: true,
}

// IssuePassportInput — the granting human comes from the session, never
// from the request: a passport is always on_behalf_of its issuer.
type IssuePassportInput struct {
	Label  *string
	Scopes []string
	TTL    *time.Duration
}

// IssuedPassport carries the raw token exactly once.
type IssuedPassport struct {
	ID        ids.PassportID
	Token     string
	Scopes    []string
	ExpiresAt time.Time
}

// InvalidScopeError maps to 422.
type InvalidScopeError struct{ Scope string }

func (e *InvalidScopeError) Error() string {
	return "scope " + e.Scope + " is not one of read|draft|write|send|enrich"
}

// IssuePassport mints a passport for the authenticated human in id.
func (s *Service) IssuePassport(ctx context.Context, id Identity, in IssuePassportInput) (IssuedPassport, error) {
	if len(in.Scopes) == 0 {
		return IssuedPassport{}, &InvalidScopeError{Scope: "(none)"}
	}
	for _, sc := range in.Scopes {
		if !validScopes[principal.Scope(sc)] {
			return IssuedPassport{}, &InvalidScopeError{Scope: sc}
		}
	}
	ttl := defaultPassportTTL
	if in.TTL != nil {
		ttl = *in.TTL
		if ttl <= 0 || ttl > maxPassportTTL {
			return IssuedPassport{}, &InvalidScopeError{Scope: fmt.Sprintf("ttl %s (max %s)", ttl, maxPassportTTL)}
		}
	}

	raw, _, err := mintSessionToken()
	if err != nil {
		return IssuedPassport{}, err
	}
	// The stored hash covers the PREFIXED token — the lookup hashes what
	// the wire carries, so there is exactly one token spelling.
	token := passportTokenPrefix + raw
	out := IssuedPassport{Token: token, Scopes: in.Scopes}

	err = database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx,
			`INSERT INTO passport (workspace_id, on_behalf_of, granted_by, label, scopes, token_hash, expires_at)
			 VALUES ($1, $2, $2, $3, $4, $5, now() + $6::interval)
			 RETURNING id, expires_at`,
			id.WorkspaceID, id.UserID, in.Label, in.Scopes, hashToken(token), ttl.String()).
			Scan(&out.ID, &out.ExpiresAt)
		if err != nil {
			return err
		}
		// Granting an agent standing authority is itself an audited fact.
		_, err = tx.Exec(ctx,
			`INSERT INTO audit_log (workspace_id, actor_type, actor_id, action, entity_type, entity_id, evidence)
			 VALUES ($1, 'human', $2, 'create', 'passport', $3,
			         jsonb_build_object('scopes', $4::text[], 'label', $5::text))`,
			id.WorkspaceID, "human:"+id.UserID.String(), out.ID, in.Scopes, in.Label)
		return err
	})
	if err != nil {
		return IssuedPassport{}, err
	}
	return out, nil
}

// PassportRow is one passport's metadata for the Settings list. The
// token hash never leaves the store — the plaintext existed exactly
// once, in the mint response.
type PassportRow struct {
	ID        ids.PassportID
	Label     *string
	Scopes    []string
	CreatedAt time.Time
	ExpiresAt time.Time
	RevokedAt *time.Time
}

// ListPassports enumerates passports as metadata: a user sees their
// own; the admin role sees the workspace's (the same authority split
// RevokePassport enforces).
func (s *Service) ListPassports(ctx context.Context, id Identity) ([]PassportRow, error) {
	isAdmin := false
	for _, r := range id.Roles {
		if r == "admin" {
			isAdmin = true
		}
	}
	var out []PassportRow
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		query := `SELECT id, label, scopes, created_at, expires_at, revoked_at
		          FROM passport WHERE on_behalf_of = $1
		          ORDER BY created_at DESC, id DESC`
		args := []any{id.UserID}
		if isAdmin {
			query = `SELECT id, label, scopes, created_at, expires_at, revoked_at
			         FROM passport
			         ORDER BY created_at DESC, id DESC`
			args = nil
		}
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p PassportRow
			if err := rows.Scan(&p.ID, &p.Label, &p.Scopes, &p.CreatedAt, &p.ExpiresAt, &p.RevokedAt); err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// RevokePassport is the kill switch: enforced at the next token lookup.
// A user revokes their own; the admin role may revoke anyone's.
func (s *Service) RevokePassport(ctx context.Context, id Identity, passportID ids.PassportID) error {
	isAdmin := false
	for _, r := range id.Roles {
		if r == "admin" {
			isAdmin = true
		}
	}
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var onBehalfOf ids.UserID
		var revokedAt *time.Time
		err := tx.QueryRow(ctx,
			`SELECT on_behalf_of, revoked_at FROM passport WHERE id = $1`, passportID).
			Scan(&onBehalfOf, &revokedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		if err != nil {
			return err
		}
		// Another user's passport reads as absent, not forbidden —
		// existence-hiding matches the row-scope convention.
		if onBehalfOf != id.UserID && !isAdmin {
			return apperrors.ErrNotFound
		}
		if revokedAt != nil {
			return nil // idempotent
		}
		if _, err := tx.Exec(ctx,
			`UPDATE passport SET revoked_at = now() WHERE id = $1`, passportID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx,
			`INSERT INTO audit_log (workspace_id, actor_type, actor_id, action, entity_type, entity_id)
			 VALUES ($1, 'human', $2, 'archive', 'passport', $3)`,
			id.WorkspaceID, "human:"+id.UserID.String(), passportID)
		return err
	})
}

// AgentIdentity is the resolved principal of a passport call: the
// passport's grants layered over the granting human's live RBAC.
type AgentIdentity struct {
	PassportID  ids.PassportID
	WorkspaceID ids.WorkspaceID
	OnBehalfOf  ids.UserID
	SeatType    string
	Scopes      principal.ScopeSet
	Roles       []string
	Teams       []ids.TeamID
	Permissions principal.Permissions
}

// Principal renders the principal shape every store entry point enforces. The
// seat is the granting human's ("agent ≤ human", A62/ADR-0047): an agent
// acting for a read seat inherits that read-only ceiling at the auth.
func (a AgentIdentity) Principal() principal.Principal {
	return principal.Principal{
		Type:        principal.PrincipalAgent,
		ID:          "agent:" + a.PassportID.String(),
		UserID:      a.OnBehalfOf.UUID,
		PassportID:  a.PassportID.UUID,
		OnBehalfOf:  a.OnBehalfOf.UUID,
		TeamIDs:     rawTeamIDs(a.Teams),
		SeatType:    principal.SeatType(a.SeatType),
		Scopes:      a.Scopes,
		Permissions: a.Permissions,
	}
}

// AuthenticateAgentByID resolves a passport ROW to its AgentIdentity —
// the trusted-process path the Surface-B scheduler uses: the worker
// holds no bearer secret, only the passport id a job row names. The
// liveness rules are identical to the token path (revocation, expiry,
// and the granting human's status all bind at resolution time), so a
// parked overnight job wakes up with exactly the authority the passport
// still has, not the authority it had when enqueued.
func (s *Service) AuthenticateAgentByID(ctx context.Context, passportID ids.PassportID) (AgentIdentity, error) {
	var a AgentIdentity
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var scopes []string
		err := tx.QueryRow(ctx,
			`SELECT p.id, p.workspace_id, p.on_behalf_of, p.scopes, u.seat_type
			 FROM passport p
			 JOIN app_user u ON u.id = p.on_behalf_of
			 WHERE p.id = $1
			   AND p.revoked_at IS NULL
			   AND now() < p.expires_at
			   AND u.status = 'active' AND u.archived_at IS NULL`,
			passportID).Scan(&a.PassportID, &a.WorkspaceID, &a.OnBehalfOf, &scopes, &a.SeatType)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		if err != nil {
			return err
		}
		a.Scopes = principal.NewScopeSet()
		for _, sc := range scopes {
			a.Scopes[principal.Scope(sc)] = struct{}{}
		}
		var loadErr error
		a.Roles, a.Teams, a.Permissions, loadErr = loadGrants(ctx, tx, a.OnBehalfOf)
		return loadErr
	})
	if err != nil {
		return AgentIdentity{}, err
	}
	return a, nil
}

// AuthenticateAgent resolves a bearer token to its AgentIdentity. The
// human's RBAC is loaded LIVE at every call — demoting or deactivating
// the human instantly narrows every passport they granted ("agent ≤
// human" is a runtime property, not a snapshot at mint time).
func (s *Service) AuthenticateAgent(ctx context.Context, rawToken string) (AgentIdentity, error) {
	if !strings.HasPrefix(rawToken, passportTokenPrefix) {
		return AgentIdentity{}, apperrors.ErrNotFound
	}

	var a AgentIdentity
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var scopes []string
		err := tx.QueryRow(ctx,
			`SELECT p.id, p.workspace_id, p.on_behalf_of, p.scopes, u.seat_type
			 FROM passport p
			 JOIN app_user u ON u.id = p.on_behalf_of
			 WHERE p.token_hash = $1
			   AND p.revoked_at IS NULL
			   AND now() < p.expires_at
			   AND u.status = 'active' AND u.archived_at IS NULL`,
			hashToken(rawToken)).Scan(&a.PassportID, &a.WorkspaceID, &a.OnBehalfOf, &scopes, &a.SeatType)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperrors.ErrNotFound
		}
		if err != nil {
			return err
		}
		a.Scopes = principal.NewScopeSet()
		for _, sc := range scopes {
			a.Scopes[principal.Scope(sc)] = struct{}{}
		}
		var loadErr error
		a.Roles, a.Teams, a.Permissions, loadErr = loadGrants(ctx, tx, a.OnBehalfOf)
		return loadErr
	})
	if err != nil {
		return AgentIdentity{}, err
	}
	return a, nil
}
