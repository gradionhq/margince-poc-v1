// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/identity/internal/ratelimit"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

const sessionCookie = "crm_session"

// Handlers is the identity module's transport surface: the identity operations of
// the contract plus the middleware that authenticates everything else.
type Handlers struct {
	svc *Service
	// seedDefaults lets other modules lay down their per-workspace defaults
	// INSIDE the bootstrap transaction, composed at the root — identity
	// never imports them. Running in-transaction makes tenant creation
	// atomic: a seed failure rolls the whole workspace back (C5).
	seedDefaults func(ctx context.Context, tx pgx.Tx) error

	// The unauthenticated endpoints carry their own throttles: login
	// attempts cost a full Argon2 verification each and bootstrap mints
	// whole tenants. Fixed windows, in-process (single-binary scope; see
	// internal/ratelimit).
	loginFailures  *ratelimit.Limiter // 10 failures/min per (email, IP)
	loginPerIP     *ratelimit.Limiter // 30/min per client IP
	bootstrapPerIP *ratelimit.Limiter // 3/hour per client IP
}

func NewHandlers(svc *Service, seedDefaults func(ctx context.Context, tx pgx.Tx) error) Handlers {
	return Handlers{
		svc:            svc,
		seedDefaults:   seedDefaults,
		loginFailures:  ratelimit.New(10, time.Minute),
		loginPerIP:     ratelimit.New(30, time.Minute),
		bootstrapPerIP: ratelimit.New(3, time.Hour),
	}
}

// clientIP is the throttle key for unauthenticated calls. RemoteAddr is
// the direct peer — behind a reverse proxy this is the proxy, so a
// deployment fronted by one must terminate rate limiting there or extend
// this to a *trusted* Forwarded header (never trusted blindly: it is
// attacker-controlled).
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// BootstrapWorkspace implements (POST /workspaces): tenant + first admin
// + session in one transaction. Unauthenticated by design.
func (h Handlers) BootstrapWorkspace(w http.ResponseWriter, r *http.Request) {
	if !h.bootstrapPerIP.Allow(clientIP(r)) {
		httperr.Write(w, r, apperrors.ErrBudgetExceeded)
		return
	}
	var req crmcontracts.BootstrapWorkspaceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httperr.Write(w, r, httperr.Validation("body", "malformed_json", err.Error()))
		return
	}
	if req.WorkspaceName == "" || req.AdminEmail == "" || len(req.AdminPassword) < 12 {
		httperr.Write(w, r, httperr.Validation("admin_password", "too_short", "workspace_name, admin_email and a ≥12-char admin_password are required"))
		return
	}

	// The seed runs INSIDE the bootstrap transaction, so if it fails the
	// tenant, admin, and roles all roll back with it — the client sees an
	// error and no partial workspace is left behind to collide on retry (C5).
	id, token, err := h.svc.Bootstrap(r.Context(), BootstrapInput{
		WorkspaceName: req.WorkspaceName,
		Slug:          slugify(req.WorkspaceName),
		AdminEmail:    string(req.AdminEmail),
		AdminName:     req.AdminDisplayName,
		AdminPassword: req.AdminPassword,
		Timezone:      deref(req.Timezone),
	}, h.seedDefaults)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}

	setSessionCookie(w, token)
	writeJSON(w, http.StatusCreated, meResponse(id))
}

// Login implements (POST /auth/login). The route is public; the workspace
// comes from the resolver middleware (subdomain slug / dev header).
func (h Handlers) Login(w http.ResponseWriter, r *http.Request) {
	var req crmcontracts.LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httperr.Write(w, r, httperr.Validation("body", "malformed_json", err.Error()))
		return
	}
	// Throttle BEFORE the Argon2 verification — the work factor that
	// protects the hash is the same one that makes unthrottled attempts
	// a memory DoS. The per-account key counts FAILURES only and pairs
	// the email with the caller's IP: counting attempts on the bare email
	// would let ten bogus posts lock the real owner out of their own
	// account from anywhere.
	accountKey := strings.ToLower(string(req.Email)) + "|" + clientIP(r)
	if !h.loginPerIP.Allow(clientIP(r)) || h.loginFailures.Blocked(accountKey) {
		httperr.Write(w, r, apperrors.ErrBudgetExceeded)
		return
	}

	id, token, err := h.svc.Login(r.Context(), string(req.Email), req.Password)
	if err != nil {
		if errors.Is(err, ErrBadCredentials) {
			h.loginFailures.Record(accountKey)
			httperr.Unauthorized(w, r, "invalid email or password")
			return
		}
		httperr.Write(w, r, err)
		return
	}

	setSessionCookie(w, token)
	writeJSON(w, http.StatusOK, meResponse(id))
}

// Logout implements (POST /auth/logout): revoke + clear, idempotent, 204.
func (h Handlers) Logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		if err := h.svc.Logout(r.Context(), cookie.Value); err != nil {
			httperr.Write(w, r, err)
			return
		}
	}
	clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// GetCurrentPrincipal implements (GET /me).
func (h Handlers) GetCurrentPrincipal(w http.ResponseWriter, r *http.Request) {
	id, ok := identityFrom(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "no session")
		return
	}
	writeJSON(w, http.StatusOK, meResponse(id))
}

// IssuePassport implements (POST /passports): the session user mints an
// agent bearer token bound to their OWN identity — on_behalf_of is never
// a request field, so a passport cannot outreach its issuer by
// construction.
func (h Handlers) IssuePassport(w http.ResponseWriter, r *http.Request) {
	id, ok := identityFrom(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "passports are minted by a signed-in human, not an agent")
		return
	}
	var req crmcontracts.IssuePassportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httperr.Write(w, r, httperr.Validation("body", "malformed_json", err.Error()))
		return
	}

	in := IssuePassportInput{Label: req.Label}
	for _, sc := range req.Scopes {
		in.Scopes = append(in.Scopes, string(sc))
	}
	if req.TtlHours != nil {
		ttl := time.Duration(*req.TtlHours) * time.Hour
		in.TTL = &ttl
	}

	issued, err := h.svc.IssuePassport(r.Context(), id, in)
	if err != nil {
		var badScope *InvalidScopeError
		if errors.As(err, &badScope) {
			httperr.Write(w, r, httperr.Validation("scopes", "invalid_scope", badScope.Error()))
			return
		}
		httperr.Write(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, crmcontracts.IssuePassportResponse{
		PassportId: openapi_types.UUID(issued.ID),
		Token:      issued.Token,
		Scopes:     issued.Scopes,
		OnBehalfOf: openapi_types.UUID(id.UserID),
		ExpiresAt:  issued.ExpiresAt,
	})
}

// RevokePassport implements (DELETE /passports/{id}): the kill switch.
func (h Handlers) RevokePassport(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	identity, ok := identityFrom(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "passports are revoked by a signed-in human")
		return
	}
	if err := h.svc.RevokePassport(r.Context(), identity, ids.UUID(id)); err != nil {
		httperr.Write(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// publicPaths need no session; every other /v1 path 401s without one
// (the middleware only fronts the API — static assets never reach it).
var publicPaths = map[string]bool{
	"/v1/workspaces":  true,
	"/v1/auth/login":  true,
	"/v1/auth/logout": true,
	// The OAuth AS endpoints authenticate by their own means: DCR is
	// open (public clients + PKCE), token exchange proves possession via
	// the code + verifier. authorize is NOT here — it demands a session.
	"/oauth/register": true,
	"/oauth/token":    true,
}

// Middleware chains workspace resolution and session authentication:
// slug → workspace GUC context; cookie → Principal. Public paths still
// get the workspace bound (login needs it), just no session requirement.
func (h Handlers) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		if slug := workspaceSlug(r); slug != "" {
			wsID, err := h.svc.ResolveWorkspace(ctx, slug)
			if err != nil && !errors.Is(err, apperrors.ErrNotFound) {
				httperr.Write(w, r, err)
				return
			}
			if err == nil {
				ctx = principal.WithWorkspaceID(ctx, wsID)
			}
		}

		if publicPaths[r.URL.Path] {
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		if _, ok := principal.WorkspaceID(ctx); !ok {
			httperr.Unauthorized(w, r, "unknown workspace")
			return
		}

		// Agents present a passport bearer token. Agent authority is
		// governed identically on every transport (ADR-0055): reads need
		// the read scope here; a MUTATING call is not refused at the
		// transport — it proceeds into the contract router, where the
		// agent gate resolves the operation's 🟢/🟡 tier against the
		// tool's declared scope and either admits, stages an approval,
		// or default-denies an un-tiered operation.
		if bearer := bearerToken(r); bearer != "" {
			agent, err := h.svc.AuthenticateAgent(ctx, bearer)
			if err != nil {
				if errors.Is(err, apperrors.ErrNotFound) {
					httperr.Unauthorized(w, r, "passport expired, revoked or unknown")
					return
				}
				httperr.Write(w, r, err)
				return
			}
			if !isMutating(r.Method) && !agent.Scopes.Has(principal.ScopeRead) {
				httperr.Write(w, r, apperrors.ErrScopeExceeded)
				return
			}
			next.ServeHTTP(w, r.WithContext(principal.WithActor(ctx, agent.Principal())))
			return
		}

		cookie, err := r.Cookie(sessionCookie)
		if err != nil {
			httperr.Unauthorized(w, r, "missing session cookie")
			return
		}
		id, err := h.svc.Authenticate(ctx, cookie.Value)
		if err != nil {
			if errors.Is(err, apperrors.ErrNotFound) {
				httperr.Unauthorized(w, r, "session expired or revoked")
				return
			}
			httperr.Write(w, r, err)
			return
		}

		// The seat ceiling is a licensing cap enforced before RBAC
		// (A62/ADR-0047): a read seat may read but never mutate over REST,
		// whatever its role grants. Method-based, matching restScope — the
		// contract has no mutating GET.
		if id.SeatType == string(principal.SeatRead) && isMutating(r.Method) {
			httperr.Write(w, r, apperrors.ErrSeatTierInsufficient)
			return
		}

		ctx = withIdentity(ctx, id)
		ctx = principal.WithActor(ctx, principal.Principal{
			Type:        principal.PrincipalHuman,
			ID:          "human:" + id.UserID.String(),
			UserID:      id.UserID,
			TeamIDs:     id.Teams,
			SeatType:    principal.SeatType(id.SeatType),
			Permissions: id.Permissions,
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// bearerToken extracts an Authorization: Bearer credential; empty when
// the request carries none.
func bearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(auth) > len(prefix) && strings.EqualFold(auth[:len(prefix)], prefix) {
		return auth[len(prefix):]
	}
	return ""
}

// restScope maps an HTTP method onto the passport verb it exercises on
// the REST surface: reads need `read`, everything mutating needs `write`.
// (send/enrich guard their own tools on the MCP surface; no REST path
// sends email today.)
func restScope(method string) principal.Scope {
	switch method {
	case http.MethodGet, http.MethodHead:
		return principal.ScopeRead
	default:
		return principal.ScopeWrite
	}
}

// isMutating is the transport-level write test the agent and read-seat
// ceilings share: everything that is not a safe read method mutates. The
// contract exposes no read-over-POST endpoint (searches are GET), so the
// method alone is authoritative here.
func isMutating(method string) bool {
	return restScope(method) != principal.ScopeRead
}

// workspaceSlug resolves the tenant slug: production uses the
// {workspace}.api.gradion.com host; local development uses the
// X-Workspace-Slug header (documented in AGENTS.md), honored ONLY under
// MARGINCE_ENV=dev — a client-controlled tenant selector must never be
// live in production, even while the RLS-scoped session lookup makes it
// unexploitable today (defense in depth).
func workspaceSlug(r *http.Request) string {
	if os.Getenv("MARGINCE_ENV") == "dev" {
		if slug := r.Header.Get("X-Workspace-Slug"); slug != "" {
			return slug
		}
	}
	host := r.Host
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	if strings.HasSuffix(host, ".api.gradion.com") {
		return strings.TrimSuffix(host, ".api.gradion.com")
	}
	return ""
}

func setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token,
		Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", MaxAge: -1,
		Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	})
}

func meResponse(id Identity) crmcontracts.MeResponse {
	roles := id.Roles
	if roles == nil {
		roles = []string{}
	}
	teams := make([]openapi_types.UUID, len(id.Teams))
	for i, t := range id.Teams {
		teams[i] = openapi_types.UUID(t)
	}
	return crmcontracts.MeResponse{
		User: crmcontracts.User{
			Id:          openapi_types.UUID(id.UserID),
			WorkspaceId: openapi_types.UUID(id.WorkspaceID),
			Email:       openapi_types.Email(id.Email),
			DisplayName: id.DisplayName,
			Status:      "active",
		},
		Roles: roles,
		Teams: teams,
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

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

func deref[T any](p *T) T {
	if p == nil {
		var zero T
		return zero
	}
	return *p
}
