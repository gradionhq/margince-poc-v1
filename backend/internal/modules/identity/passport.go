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
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
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

// MaxOAuthAccessTokenTTL is the ceiling mintPassport admits a TTL against,
// exported so a process role can refuse an out-of-range
// --oauth-access-token-ttl while it boots rather than leaving the first
// connector handshake to discover it.
const MaxOAuthAccessTokenTTL = maxPassportTTL

// passportScopeVocabulary is the closed verb vocabulary (interfaces.md §2), in
// ascending authority order. It is the ONE list: admission (validScopes) and
// discovery (oauthScopesSupported) are both derived from it, so a scope added
// here cannot be grantable-but-undiscoverable — a scope a client cannot see in
// the metadata is a scope it will never ask for.
var passportScopeVocabulary = []principal.Scope{
	principal.ScopeRead, principal.ScopeDraft, principal.ScopeWrite,
	principal.ScopeSend, principal.ScopeEnrich,
}

// validScopes is the admission form of that vocabulary: the mint and the
// authorize parser both test membership, and neither may accept a verb the
// metadata does not advertise.
var validScopes = func() map[principal.Scope]bool {
	admitted := make(map[principal.Scope]bool, len(passportScopeVocabulary))
	for _, scope := range passportScopeVocabulary {
		admitted[scope] = true
	}
	return admitted
}()

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

// IssuePassport mints a passport for the authenticated human in id — the
// A1/local path, where the passport answers to no OAuth grant.
func (s *Service) IssuePassport(ctx context.Context, id Identity, in IssuePassportInput) (IssuedPassport, error) {
	var out IssuedPassport
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = mintPassport(ctx, tx, id, in, nil)
		return err
	})
	if err != nil {
		return IssuedPassport{}, err
	}
	return out, nil
}

// mintPassport is the ONE spelling of the passport-mint write: admission
// (the closed scope vocabulary and the TTL ceiling), the row, and the
// audit row that records granting an agent standing authority. Admission
// lives inside it rather than in each caller so no issuance path can
// forget it.
//
// It takes the CALLER's transaction because the A2 code exchange commits
// the mint together with the grant the passport belongs to and the
// authorization code it spent — a passport whose grant did not commit
// would be live authority nothing can revoke. grantID is nil for a
// locally minted passport, which answers to no grant.
func mintPassport(ctx context.Context, tx pgx.Tx, id Identity, in IssuePassportInput, grantID *ids.UUID) (IssuedPassport, error) {
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

	if err := tx.QueryRow(ctx,
		`INSERT INTO passport (workspace_id, on_behalf_of, granted_by, label, scopes, token_hash, expires_at, oauth_grant_id)
		 VALUES ($1, $2, $2, $3, $4, $5, now() + $6::interval, $7)
		 RETURNING id, expires_at`,
		id.WorkspaceID, id.UserID, in.Label, in.Scopes, hashToken(token), ttl.String(), grantID).
		Scan(&out.ID, &out.ExpiresAt); err != nil {
		return IssuedPassport{}, err
	}
	// Granting an agent standing authority is itself an audited fact. The
	// scopes and label are the row's own fields, so they are its after image
	// rather than evidence about the write.
	if _, err := storekit.Audit(actorCtx(ctx, id), tx, "create", "passport", out.ID.UUID,
		nil, map[string]any{"scopes": in.Scopes, "label": in.Label}); err != nil {
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
	isAdmin := id.hasRole("admin")
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

// RevokePassport is the kill switch: enforced at the next token lookup
// (every MCP/REST agent call re-authenticates the passport row), and
// published as passport.revoked (events.md §5.6a) so long-lived
// consumers — in-flight agent sessions, read-models — drop it within
// one bus cycle. A user revokes their own; the admin role may revoke
// anyone's.
//
// A passport issued under an OAuth grant is not an independent credential:
// it is ONE credential of a connection, and the client's next refresh would
// mint a replacement seconds after the human killed it. So revoking it ends
// the whole connection through the ONE cascade — which takes the grant lock
// as its first act, keeping the grant → refresh → passport order this
// function must not invert.
func (s *Service) RevokePassport(ctx context.Context, id Identity, passportID ids.PassportID) error {
	isAdmin := id.hasRole("admin")
	ctx = actorCtx(ctx, id)
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var onBehalfOf ids.UserID
		var revokedAt *time.Time
		var grantID *ids.UUID
		err := tx.QueryRow(ctx,
			`SELECT on_behalf_of, revoked_at, oauth_grant_id FROM passport WHERE id = $1`, passportID).
			Scan(&onBehalfOf, &revokedAt, &grantID)
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
		if revokedAt != nil && grantID == nil {
			return nil // idempotent: a locally minted passport has nothing beneath it
		}
		if grantID != nil {
			// The connection dies even when THIS row is already dead. A rotation
			// that replaced the passport a moment before the human pressed the
			// button would otherwise turn their revoke into a no-op: the
			// credential they aimed at is gone, and the connection they meant
			// keeps serving calls under its successor.
			if err := s.revokeGrantTx(ctx, tx, *grantID, passportRevokedReason); err != nil {
				return err
			}
		}
		// The row itself, conditional on it still being live: the cascade
		// above already retired every passport under the grant, and a second
		// UPDATE would audit one death twice. It still runs for a passport
		// whose grant was already dead — nothing may report a revocation it
		// did not perform.
		tag, err := tx.Exec(ctx,
			`UPDATE passport SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`, passportID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		return auditPassportRevoked(ctx, tx, passportID, id.UserID)
	})
}

// auditPassportRevoked records one dead passport: the audit row and the bus
// fact. It is the ONE spelling, so a credential a human deleted and one a
// cascade retired are indistinguishable to a consumer holding it — what a
// consumer has to act on is that THIS passport is gone, never why.
//
// agent_connection_id is omitted: the A1/local issuance path has no
// agent-connection storage (the hosted A2 surface adds it).
func auditPassportRevoked(ctx context.Context, tx pgx.Tx, passportID ids.PassportID, by ids.UserID) error {
	auditID, err := storekit.Audit(ctx, tx, "archive", "passport", passportID.UUID, nil, nil)
	if err != nil {
		return err
	}
	return storekit.EmitEvent(ctx, tx, auditID, passportID.UUID,
		passportRevokedPayload(passportID, by))
}

// passportRevokedPayload builds passport.revoked's typed payload.
func passportRevokedPayload(passportID ids.PassportID, by ids.UserID) crmcontracts.PublicEventPassportRevoked {
	return crmcontracts.PublicEventPassportRevoked{
		PassportId: openapi_types.UUID(passportID.UUID),
		By:         openapi_types.UUID(by.UUID),
	}
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

// liveClientPredicate is the ONE spelling of "this client is still a client",
// carried by EVERY statement that reads oauth_client: authentication (the rule
// below) and issuance alike (the consent form, the consent POST and the code
// exchange, in oauth.go and oauth_token.go). Disable and soft-delete are the
// operator's off switch, and a switch that only stops calls — while consent and
// issuance carry on beneath it — spends a human's approval on a client an admin
// already killed. The client table is aliased c in each of those statements so
// this is one string rather than four that can rot apart.
const liveClientPredicate = `c.disabled_at IS NULL AND c.deleted_at IS NULL`

// The LEFT JOINs and the predicate below are the liveness rule for an
// OAuth-issued passport: a revoked grant, or a disabled or soft-deleted
// client, must stop the credential on the very next call. They are the
// answer for anything the revocation cascade never saw — a row written
// before the cascade existed, or a grant an operator killed in the store —
// so authentication fails closed instead of trusting that every kill path
// remembered to walk down here.
//
// c.client_id IS NOT NULL is load-bearing: a LEFT JOIN that found no client
// row means the grant points at a client that no longer exists, which must
// fail closed rather than pass for want of a disabled_at to read.
const agentLivenessJoins = `
	LEFT JOIN oauth_grant  g ON (g.workspace_id, g.id)        = (p.workspace_id, p.oauth_grant_id)
	LEFT JOIN oauth_client c ON (c.workspace_id, c.client_id) = (g.workspace_id, g.client_id)`

// A locally minted passport (oauth_grant_id IS NULL) answers to no grant and
// is unaffected — the A1 path must keep working exactly as it did. The client
// half is liveClientPredicate (oauth.go), the same string the issuance path
// carries, so "still a client" cannot mean one thing at consent and another at
// authentication.
const agentLivenessPredicate = `
	AND (p.oauth_grant_id IS NULL
	     OR (g.revoked_at IS NULL AND c.client_id IS NOT NULL
	         AND ` + liveClientPredicate + `))`

// The two entry points differ in NOTHING but which column identifies the
// passport — the presented token's hash on the wire path, the row id on the
// trusted-process path — so that difference is all each one supplies.
const (
	agentByHashPredicate = `p.token_hash = $1`
	agentByIDPredicate   = `p.id = $1`
)

// agentAuthQuery assembles the agent-authentication statement around one
// caller's predicate. Both entry points build theirs here, which is what
// makes the liveness rule above impossible to have on one path and miss on
// the other.
func agentAuthQuery(predicate string) string {
	return `SELECT p.id, p.workspace_id, p.on_behalf_of, p.scopes, u.seat_type
		FROM passport p
		JOIN app_user u ON u.id = p.on_behalf_of` + agentLivenessJoins + `
		WHERE ` + predicate + `
		  AND p.revoked_at IS NULL
		  AND now() < p.expires_at
		  AND u.status = 'active' AND u.archived_at IS NULL` + agentLivenessPredicate
}

// authenticateAgentWhere is the ONE agent-authentication query, resolving a
// live passport to the principal its calls carry: the passport's own scopes
// over the granting human's RBAC, loaded here and now.
//
//craft:ignore naked-any a pgx query argument is untyped by the driver's own signature, and the two callers pass different column types
func (s *Service) authenticateAgentWhere(ctx context.Context, tx pgx.Tx, predicate string, arg any) (AgentIdentity, error) {
	var a AgentIdentity
	var scopes []string
	err := tx.QueryRow(ctx, agentAuthQuery(predicate), arg).
		Scan(&a.PassportID, &a.WorkspaceID, &a.OnBehalfOf, &scopes, &a.SeatType)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentIdentity{}, apperrors.ErrNotFound
	}
	if err != nil {
		return AgentIdentity{}, err
	}
	a.Scopes = principal.NewScopeSet()
	for _, sc := range scopes {
		a.Scopes[principal.Scope(sc)] = struct{}{}
	}
	var loadErr error
	a.Roles, a.Teams, a.Permissions, loadErr = loadGrants(ctx, tx, a.OnBehalfOf)
	if loadErr != nil {
		return AgentIdentity{}, loadErr
	}
	return a, nil
}

// AuthenticateAgentByID resolves a passport ROW to its AgentIdentity —
// the trusted-process path the Surface-B scheduler uses: the worker
// holds no bearer secret, only the passport id a job row names. The
// liveness rules are identical to the token path (revocation, expiry,
// the granting human's status and the connection the passport belongs to
// all bind at resolution time), so a parked overnight job wakes up with
// exactly the authority the passport still has, not the authority it had
// when enqueued.
func (s *Service) AuthenticateAgentByID(ctx context.Context, passportID ids.PassportID) (AgentIdentity, error) {
	var a AgentIdentity
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		a, err = s.authenticateAgentWhere(ctx, tx, agentByIDPredicate, passportID)
		return err
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
		var err error
		a, err = s.authenticateAgentWhere(ctx, tx, agentByHashPredicate, hashToken(rawToken))
		return err
	})
	if err != nil {
		return AgentIdentity{}, err
	}
	return a, nil
}
