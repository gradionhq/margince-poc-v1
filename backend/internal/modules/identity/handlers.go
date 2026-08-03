// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/platform/httpserver"
	"github.com/gradionhq/margince/backend/internal/platform/mailer"
	"github.com/gradionhq/margince/backend/internal/platform/ratelimit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// SessionCookieName is the cookie a signed-in human's browser carries. It is
// exported because the connector's edge meters the consent flow on the session a
// request presents (compose/oauthedge.go) and sits outside this middleware, so
// the cookie name has to be one spelling shared with it rather than two that can
// drift.
const SessionCookieName = "crm_session"

// Handlers is the identity module's transport surface: the identity operations of
// the contract plus the middleware that authenticates everything else.
type Handlers struct {
	svc *Service
	// resetMailer + resetBaseURL wire the A74 forgot-password flow; nil
	// mailer means the flow is absent — the endpoints answer 501 and the
	// capabilities probe reports password_reset=false, so the login UI
	// never renders a link this surface cannot honor (A107).
	resetMailer  mailer.Mailer
	resetBaseURL string
	// resetSendStarted is a test seam: the async reset send signals here
	// when it finishes, so a test can wait for the captured mail without
	// sleeping. Nil in production.
	resetSendStarted func()

	// oidc is the configured federated sign-in provider (A107/ADR-0061 §6);
	// nil means the installation configured none — the two OIDC endpoints
	// answer 404 and the capabilities probe lists no provider, so the login
	// screen draws no button it cannot honor.
	oidc *OIDCLogin
	// passwordDisabled turns email+password sign-in OFF, which an operator may
	// do only once a federated provider can carry the installation
	// (deployconfig refuses to disable the last method). Spelled negatively so
	// the zero Handlers value keeps password login enabled — the posture every
	// role and every test that wires nothing must get.
	//
	// It gates the whole password FAMILY: login and both recovery endpoints. A
	// reset link that still mints a session would be the bypass the operator
	// turned password off to close.
	passwordDisabled bool

	// The unauthenticated endpoints carry their own throttles: login
	// attempts cost a full Argon2 verification each and reset requests
	// cost the operator an outbound mail. Fixed windows, in-process
	// (single-binary scope; see platform/ratelimit).
	loginFailures *ratelimit.Limiter // 10 failures/min per (email, IP)
	loginPerIP    *ratelimit.Limiter // 30/min per client IP
	resetPerEmail *ratelimit.Limiter // 3/hour per (email, IP)
	resetPerIP    *ratelimit.Limiter // 30/hour per client IP
	oidcPerIP     *ratelimit.Limiter // 30/min per client IP — each start costs a provider round-trip and a state row

	// sorMode answers whether the caller's workspace reads from an
	// incumbent overlay mirror, so /me can tell the client its
	// system-of-record mode (the client gates its list UI on it — an
	// overlay mirror cannot serve sort/filter dials). Injected by the
	// composition root (the datasource dispatch owns mode resolution;
	// identity never imports the overlay module). Nil ⟹ always native,
	// the correct default for any role that wired no overlay dispatch.
	sorMode func(context.Context) (overlay bool, err error)

	// mcpResource is the canonical MCP server URL (public_base_url +
	// "/mcp"), injected by the composition root from deployment config.
	// The RFC 9728 protected-resource document advertises this verbatim
	// as "resource" — never the request origin, which an attacker
	// controls via Host/X-Forwarded-Proto and which an OAuth audience
	// decision must not depend on.
	mcpResource string

	// oauthAccessTokenTTL is the operator's lifetime for an OAuth-minted
	// passport, from --oauth-access-token-ttl. Zero means unset, and an
	// unset TTL keeps the mint's own default: a connector's access token
	// is a 30-day passport unless an operator shortens it, which is the
	// posture every deployment had before the flag existed. It applies to
	// BOTH mints of a connection's life — the code exchange and every
	// rotation — because a short-lived access token an hour-old rotation
	// re-issues for 30 days is not short-lived.
	oauthAccessTokenTTL time.Duration
}

// NewHandlers builds the identity transport surface over its service.
func NewHandlers(svc *Service) Handlers {
	return Handlers{
		svc:           svc,
		loginFailures: ratelimit.New(10, time.Minute),
		loginPerIP:    ratelimit.New(30, time.Minute),
		resetPerEmail: ratelimit.New(3, time.Hour),
		resetPerIP:    ratelimit.New(30, time.Hour),
		oidcPerIP:     ratelimit.New(30, time.Minute),
	}
}

// WithPasswordReset wires the forgot-password flow: the outbound-email
// transport and the public base the emailed link points at. Wired by
// the composition root when (and only when) the operator configured
// email — absent it the flow stays its explicit 501.
func (h Handlers) WithPasswordReset(m mailer.Mailer, publicBaseURL string) Handlers {
	h.resetMailer = m
	h.resetBaseURL = strings.TrimRight(publicBaseURL, "/")
	return h
}

// WithPasswordLogin sets whether email+password sign-in is offered. The
// composition root passes the deployment's `auth.password.enabled`; every
// role that passes nothing keeps it on, which is the default posture.
func (h Handlers) WithPasswordLogin(enabled bool) Handlers {
	h.passwordDisabled = !enabled
	return h
}

// passwordEnabled answers whether the password family may be served at all.
func (h Handlers) passwordEnabled() bool {
	return !h.passwordDisabled
}

// WithSorMode injects the workspace system-of-record mode resolver the
// composition root builds over the datasource dispatch. Without it /me
// reports native (the correct answer for any role with no overlay wiring).
func (h Handlers) WithSorMode(resolve func(context.Context) (bool, error)) Handlers {
	h.sorMode = resolve
	return h
}

// WithMCPResource injects the canonical MCP resource URL the RFC 9728
// protected-resource document advertises. The composition root computes
// it from --public-base-url, never from a request, so the audience the
// OAuth handshake protects can never be steered by an attacker-controlled
// Host header.
func (h Handlers) WithMCPResource(resource string) Handlers {
	h.mcpResource = resource
	return h
}

// WithOAuthAccessTokenTTL sets how long a passport minted through the OAuth
// handshake lives. Connector norms are minutes plus refresh, while a passport
// defaults to 30 days; this is the knob that lets an operator take that to
// 15m without a code change, now that the refresh machinery makes a short
// lifetime cheap. Zero leaves the default alone.
func (h Handlers) WithOAuthAccessTokenTTL(ttl time.Duration) Handlers {
	h.oauthAccessTokenTTL = ttl
	return h
}

// accessTokenTTL is what the two OAuth mints pass to mintPassport: nil when no
// operator TTL is configured, so the mint applies its own default rather than
// this package deciding the number twice.
func (h Handlers) accessTokenTTL() *time.Duration {
	if h.oauthAccessTokenTTL == 0 {
		return nil
	}
	ttl := h.oauthAccessTokenTTL
	return &ttl
}

// resolveSorMode names the caller's workspace system-of-record mode for
// the /me response. A nil resolver (no overlay wiring) is native; a
// resolver error degrades to native rather than failing /me — the 422
// read-subset guard still refuses any dial the mirror cannot serve, so a
// momentary mis-report costs an unsorted list, never a wrong answer.
func (h Handlers) resolveSorMode(ctx context.Context) crmcontracts.MeResponseSystemOfRecordMode {
	if h.sorMode == nil {
		return crmcontracts.Native
	}
	overlay, err := h.sorMode(ctx)
	if err != nil || !overlay {
		return crmcontracts.Native
	}
	return crmcontracts.Overlay
}

// GetAuthCapabilities implements (GET /auth/capabilities): the anonymous
// probe the login UI renders from (A107/ADR-0061). It reports exactly the
// operational methods — a disabled provider button or a dead
// "Forgot password?" link is a misleading affordance — and discloses
// nothing beyond what the login UI needs.
func (h Handlers) GetAuthCapabilities(w http.ResponseWriter, r *http.Request) {
	caps := crmcontracts.AuthCapabilities{
		Password: h.passwordEnabled(),
		// Reported from the SAME field the endpoints refuse on: an
		// installation that turned password login off offers no way back in
		// through a reset link either, so advertising one would be a dead
		// affordance twice over.
		PasswordReset: h.passwordEnabled() && h.resetMailer != nil,
	}
	caps.OidcProviders = make([]struct {
		Key   string `json:"key"`
		Label string `json:"label"`
	}, 0, 1)
	if h.oidc != nil {
		// Listed because the flow behind it is wired, not because a provider
		// is named in configuration: h.oidc exists only once the composition
		// root built a usable relying party.
		caps.OidcProviders = append(caps.OidcProviders, struct {
			Key   string `json:"key"`
			Label string `json:"label"`
		}{Key: h.oidc.key, Label: h.oidc.label})
	}
	httperr.WriteJSON(w, http.StatusOK, caps)
}

// Login implements (POST /auth/login). The route is public; the singleton
// organization is bound by the middleware (installation.go).
func (h Handlers) Login(w http.ResponseWriter, r *http.Request) {
	if !h.passwordEnabled() {
		// An installation that authenticates through a provider does not
		// accept a password from anyone — refused BEFORE the body is read, so
		// no credential is even parsed on a surface that cannot honor one.
		// The same 501 shape the reset flow uses for an absent method, and the
		// capabilities probe already told the login screen not to offer it.
		httperr.NotImplemented(w, r, "Login")
		return
	}
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
	accountKey := strings.ToLower(string(req.Email)) + "|" + httpserver.ClientIP(r)
	if !h.loginPerIP.Allow(httpserver.ClientIP(r)) || h.loginFailures.Blocked(accountKey) {
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
	httperr.WriteJSON(w, http.StatusOK, meResponse(id, h.resolveSorMode(r.Context())))
}

// Logout implements (POST /auth/logout): revoke + clear, idempotent, 204.
func (h Handlers) Logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(SessionCookieName); err == nil {
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
	httperr.WriteJSON(w, http.StatusOK, meResponse(id, h.resolveSorMode(r.Context())))
}

// serveAsAgent admits a passport bearer under the agent principal. ctx is
// the workspace-resolved context; it lands on the request exactly once,
// at the hand-off to next.
func (h Handlers) serveAsAgent(ctx context.Context, w http.ResponseWriter, r *http.Request, next http.Handler, bearer string) {
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
}

// serveAsHuman resolves the session cookie to a human principal and
// enforces the seat ceiling before the request reaches RBAC. ctx is the
// workspace-resolved context; it lands on the request exactly once, at
// the hand-off to next.
func (h Handlers) serveAsHuman(ctx context.Context, w http.ResponseWriter, r *http.Request, next http.Handler) {
	cookie, err := r.Cookie(SessionCookieName)
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

	next.ServeHTTP(w, r.WithContext(withHumanPrincipal(ctx, id)))
}

// withHumanPrincipal binds one authenticated human onto the context: the
// identity the module's own handlers read and the kernel principal every store
// gate reads. One spelling for both hand-offs below, so a session admitted by
// either arrives as the same principal.
func withHumanPrincipal(ctx context.Context, id Identity) context.Context {
	return principal.WithActor(withIdentity(ctx, id), principal.Principal{
		Type:        principal.PrincipalHuman,
		ID:          "human:" + id.UserID.String(),
		UserID:      id.UserID.UUID,
		TeamIDs:     rawTeamIDs(id.Teams),
		SeatType:    principal.SeatType(id.SeatType),
		Permissions: id.Permissions,
	})
}

// serveAsOptionalHuman serves the consent entry point (isConsentEntry,
// middleware.go) with whatever session the browser has, including none: a
// signed-in human reaches the handler as themselves, and a human who is not
// signed in reaches it as nobody — which is the case the handler answers with a
// redirect to the login screen.
//
// "Not signed in" deliberately covers an expired or revoked session too. The
// human's situation is identical (they must sign in again) and so is the answer,
// while a 401 would strand exactly the human whose consent screen sat open too
// long. Nothing is admitted by it: this route hands an unidentified caller a
// redirect and nothing else, and the seat ceiling has no bearing on a GET.
//
// A session that cannot be RESOLVED — a database failure, not a dead session —
// is still an error. Reporting it as "not signed in" would send a human into a
// login loop against an installation that cannot authenticate anyone.
func (h Handlers) serveAsOptionalHuman(ctx context.Context, w http.ResponseWriter, r *http.Request, next http.Handler) {
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil {
		next.ServeHTTP(w, r.WithContext(ctx))
		return
	}
	id, err := h.svc.Authenticate(ctx, cookie.Value)
	if errors.Is(err, apperrors.ErrNotFound) {
		next.ServeHTTP(w, r.WithContext(ctx))
		return
	}
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	next.ServeHTTP(w, r.WithContext(withHumanPrincipal(ctx, id)))
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

func setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookieName, Value: token,
		Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookieName, Value: "", MaxAge: -1,
		Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	})
}

func meResponse(id Identity, sorMode crmcontracts.MeResponseSystemOfRecordMode) crmcontracts.MeResponse {
	roles := id.Roles
	if roles == nil {
		roles = []string{}
	}
	teams := make([]openapi_types.UUID, len(id.Teams))
	for i, t := range id.Teams {
		teams[i] = openapi_types.UUID(t.UUID)
	}
	return crmcontracts.MeResponse{
		User: crmcontracts.User{
			Id:          openapi_types.UUID(id.UserID.UUID),
			WorkspaceId: openapi_types.UUID(id.WorkspaceID.UUID),
			Email:       openapi_types.Email(id.Email),
			DisplayName: id.DisplayName,
			Status:      "active",
		},
		Roles: roles,
		Teams: teams,
		SystemOfRecord: &struct {
			Mode crmcontracts.MeResponseSystemOfRecordMode `json:"mode"`
		}{Mode: sorMode},
	}
}
