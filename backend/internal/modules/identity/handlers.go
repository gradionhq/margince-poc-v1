// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/platform/mailer"
	"github.com/gradionhq/margince/backend/internal/platform/ratelimit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

const sessionCookie = "crm_session"

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

	// The unauthenticated endpoints carry their own throttles: login
	// attempts cost a full Argon2 verification each and reset requests
	// cost the operator an outbound mail. Fixed windows, in-process
	// (single-binary scope; see platform/ratelimit).
	loginFailures *ratelimit.Limiter // 10 failures/min per (email, IP)
	loginPerIP    *ratelimit.Limiter // 30/min per client IP
	resetPerEmail *ratelimit.Limiter // 3/hour per (email, IP)
	resetPerIP    *ratelimit.Limiter // 30/hour per client IP

	// sorMode answers whether the caller's workspace reads from an
	// incumbent overlay mirror, so /me can tell the client its
	// system-of-record mode (the client gates its list UI on it — an
	// overlay mirror cannot serve sort/filter dials). Injected by the
	// composition root (the datasource dispatch owns mode resolution;
	// identity never imports the overlay module). Nil ⟹ always native,
	// the correct default for any role that wired no overlay dispatch.
	sorMode func(context.Context) (overlay bool, err error)
}

// NewHandlers builds the identity transport surface over its service.
func NewHandlers(svc *Service) Handlers {
	return Handlers{
		svc:           svc,
		loginFailures: ratelimit.New(10, time.Minute),
		loginPerIP:    ratelimit.New(30, time.Minute),
		resetPerEmail: ratelimit.New(3, time.Hour),
		resetPerIP:    ratelimit.New(30, time.Hour),
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

// WithSorMode injects the workspace system-of-record mode resolver the
// composition root builds over the datasource dispatch. Without it /me
// reports native (the correct answer for any role with no overlay wiring).
func (h Handlers) WithSorMode(resolve func(context.Context) (bool, error)) Handlers {
	h.sorMode = resolve
	return h
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

// GetAuthCapabilities implements (GET /auth/capabilities): the anonymous
// probe the login UI renders from (A107/ADR-0061). It reports exactly the
// operational methods — a disabled provider button or a dead
// "Forgot password?" link is a misleading affordance — and discloses
// nothing beyond what the login UI needs.
func (h Handlers) GetAuthCapabilities(w http.ResponseWriter, r *http.Request) {
	caps := crmcontracts.AuthCapabilities{
		Password:      true,
		PasswordReset: h.resetMailer != nil,
	}
	caps.OidcProviders = make([]struct {
		Key   string `json:"key"`
		Label string `json:"label"`
	}, 0)
	httperr.WriteJSON(w, http.StatusOK, caps)
}

// Login implements (POST /auth/login). The route is public; the singleton
// organization is bound by the middleware (installation.go).
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
	httperr.WriteJSON(w, http.StatusOK, meResponse(id, h.resolveSorMode(r.Context())))
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
	httperr.WriteJSON(w, http.StatusOK, meResponse(id, h.resolveSorMode(r.Context())))
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
	httperr.WriteJSON(w, http.StatusCreated, crmcontracts.IssuePassportResponse{
		PassportId: openapi_types.UUID(issued.ID.UUID),
		Token:      issued.Token,
		Scopes:     issued.Scopes,
		OnBehalfOf: openapi_types.UUID(id.UserID.UUID),
		ExpiresAt:  issued.ExpiresAt,
	})
}

// ListPassports implements (GET /passports): passport metadata for the
// Settings list. Tokens are never re-disclosed; agent_id and
// last_used_at have no storage yet and read as absent (recorded in the
// batch's decision file).
func (h Handlers) ListPassports(w http.ResponseWriter, r *http.Request) {
	identity, ok := identityFrom(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "passports are listed by a signed-in human")
		return
	}
	rows, err := h.svc.ListPassports(r.Context(), identity)
	if err != nil {
		httperr.Write(w, r, err)
		return
	}
	data := make([]crmcontracts.PassportSummary, 0, len(rows))
	for _, p := range rows {
		summary := crmcontracts.PassportSummary{
			Id:        openapi_types.UUID(p.ID.UUID),
			Scopes:    p.Scopes,
			CreatedAt: p.CreatedAt,
			ExpiresAt: &p.ExpiresAt,
			RevokedAt: p.RevokedAt,
		}
		if p.Label != nil {
			summary.Label = *p.Label
		}
		data = append(data, summary)
	}
	httperr.WriteJSON(w, http.StatusOK, struct {
		Data []crmcontracts.PassportSummary `json:"data"`
	}{Data: data})
}

// RevokePassport implements (DELETE /passports/{id}): the kill switch.
func (h Handlers) RevokePassport(w http.ResponseWriter, r *http.Request, id crmcontracts.Id) {
	identity, ok := identityFrom(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "passports are revoked by a signed-in human")
		return
	}
	if err := h.svc.RevokePassport(r.Context(), identity, ids.From[ids.PassportKind](ids.UUID(id))); err != nil {
		httperr.Write(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
		UserID:      id.UserID.UUID,
		TeamIDs:     rawTeamIDs(id.Teams),
		SeatType:    principal.SeatType(id.SeatType),
		Permissions: id.Permissions,
	})
	next.ServeHTTP(w, r.WithContext(ctx))
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
