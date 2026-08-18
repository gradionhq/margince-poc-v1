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
	"github.com/gradionhq/margince/backend/internal/shared/buildinfo"
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
	// resetMailer is the A74 outbound-email transport. Nil means the
	// installation has no email channel: forgot-password is absent (its
	// endpoint answers 501) and the capabilities probe reports
	// password_reset=false, so the login UI never renders a self-service
	// link this surface cannot honor (A107).
	resetMailer mailer.Mailer
	// passwordLinkBaseURL is the canonical external base every set-password
	// deep link is built on — the mailed reset link AND the admin-issued one
	// (ADR-0061 Amendment 1). It is a property of the INSTALLATION, not of
	// the mail flow, which is why it arrives separately: an installation with
	// no mailer still needs it, and that is exactly where the admin-issued
	// link lives. Empty means no link of either kind can be built.
	passwordLinkBaseURL string
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
	// changeFailures caps wrong-current-password attempts per account.
	// /auth/change-password verifies the SAME secret the login path does, so
	// leaving it uncapped would put an unthrottled guessing oracle behind any
	// borrowed session — the login route's own cap exists for exactly that
	// secret. Keyed by user id rather than IP: the account is what is being
	// guessed at, and a caller who already holds a session can change IP.
	changeFailures *ratelimit.Limiter // 10 failures/min per user

	// Issuing a set-password link is authenticated and admin-only, so these
	// two are not anti-anonymous-abuse throttles like the pair above — they
	// bound what a STOLEN admin session can do. Each issue supersedes the
	// target's outstanding token, which makes an unbounded operation a
	// denial-of-recovery primitive: repeat it against one member and their
	// link is invalidated as fast as an admin can hand it over. Per target
	// as well as per actor, because a single compromised admin churning one
	// member's credential is the case the per-actor bound alone still admits
	// at full rate.
	passwordLinkPerActor  *ratelimit.Limiter // 20/hour per admin
	passwordLinkPerTarget *ratelimit.Limiter // 5/hour per member

	// sorMode answers whether the caller's workspace reads from an
	// incumbent overlay mirror, so /me can tell the client its
	// system-of-record mode (the client gates its list UI on it — an
	// overlay mirror cannot serve sort/filter dials). Injected by the
	// composition root (the datasource dispatch owns mode resolution;
	// identity never imports the overlay module). Nil ⟹ always native,
	// the correct default for any role that wired no overlay dispatch.
	sorMode func(context.Context) (overlay bool, err error)

	// nonProduction reports the deployment posture (MARGINCE_ENV) on /me's
	// deprecated non_production field. Injected by the composition root from
	// runtimeenv.Environment.IsNonProduction() — identity never imports
	// deployconfig or compose. It gates nothing: what a client may DO is
	// dataResetAvailable below.
	nonProduction bool
	// dataResetAvailable is operations.allow_data_reset — whether this
	// installation armed the destructive reset at all. The same value the
	// endpoint gates on, so an offered action and a served route agree. False
	// is the fail-closed default for a role that wired nothing.
	dataResetAvailable bool
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
		svc:                   svc,
		loginFailures:         ratelimit.New(10, time.Minute),
		loginPerIP:            ratelimit.New(30, time.Minute),
		resetPerEmail:         ratelimit.New(3, time.Hour),
		resetPerIP:            ratelimit.New(30, time.Hour),
		changeFailures:        ratelimit.New(10, time.Minute),
		passwordLinkPerActor:  ratelimit.New(20, time.Hour),
		passwordLinkPerTarget: ratelimit.New(5, time.Hour),
	}
}

// ResetRateLimits clears the auth lockout buckets. The non-production data
// reset calls it so the admin who just wiped the installation is not held out
// of the login and password-reset flows by counters the wipe could not reach.
// The per-credential throughput ceilings elsewhere are not lockouts and are
// left running.
//
// A handler set that did not come from NewHandlers carries no limiters, and then
// there is nothing to clear: it must say so by doing nothing, because the caller
// is a reset handler whose panic would reach an operator as an opaque 500 on a
// wipe that had otherwise finished.
func (h *Handlers) ResetRateLimits() {
	for _, bucket := range []*ratelimit.Limiter{h.loginFailures, h.loginPerIP, h.resetPerEmail, h.resetPerIP, h.changeFailures} {
		if bucket != nil {
			bucket.Reset()
		}
	}
}

// WithPasswordReset wires the forgot-password flow's outbound-email
// transport. Wired by the composition root when (and only when) the
// operator configured email — absent it the flow stays its explicit 501.
// The link base arrives separately via WithPasswordLinkBase, because an
// installation without email still builds set-password links.
func (h Handlers) WithPasswordReset(m mailer.Mailer) Handlers {
	h.resetMailer = m
	return h
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
//
// The release version is part of what the login UI needs, because the web tier
// cannot compare without it (compose/releaseversion.go carries why there is
// anything to compare). An unstamped build reports NOTHING rather than an empty
// string: absence is what the contract gives a client permission to ignore, and
// an empty value would be a version the client then has to know is not one.
func (h Handlers) GetAuthCapabilities(w http.ResponseWriter, r *http.Request) {
	caps := crmcontracts.AuthCapabilities{
		Password:      true,
		PasswordReset: h.canSendPasswordLink(),
	}
	if buildinfo.Comparable(buildinfo.ReleaseVersion) {
		// A local copy because the contract field is optional and therefore a
		// pointer; absence is the answer for an unstamped build.
		release := buildinfo.ReleaseVersion
		caps.ReleaseVersion = &release
	}
	caps.OidcProviders = make([]struct {
		Key   string `json:"key"`
		Label string `json:"label"`
	}, 0)
	// NO-STORE, and the release version is what makes it mandatory rather than
	// tidy. This response is not per-principal, so a shared cache leaks nothing —
	// but the SPA refuses to render at all when the release it reads here differs
	// from its own, so one stale copy held by any cache on this origin turns a
	// healthy installation into the mixed-release screen for every reader served
	// from it, and reloading cannot clear it. A validator-less 200 GET is exactly
	// what an intermediary assigns heuristic freshness to.
	w.Header().Set("Cache-Control", "no-store")
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
	httperr.WriteJSON(w, http.StatusOK, h.meResponse(id, h.resolveSorMode(r.Context())))
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
//
// Human sessions only. A passport bearer is admitted as an agent principal and
// never binds the session identity read here (serveAsAgent below), so an agent
// arrives without one and is answered 401 rather than a partial profile — which
// is why the contract's passport claim is deprecated as permanently null.
func (h Handlers) GetCurrentPrincipal(w http.ResponseWriter, r *http.Request) {
	id, ok := identityFrom(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "no session")
		return
	}
	// The authorization snapshot is this principal's alone and outlives nothing:
	// a shared cache that served it to the next caller would hand them someone
	// else's capabilities, and a stored copy would survive the role change that
	// revoked them.
	w.Header().Set("Cache-Control", "private, no-store")
	httperr.WriteJSON(w, http.StatusOK, h.meResponse(id, h.resolveSorMode(r.Context())))
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

// meResponse renders /me for one principal. It is a method rather than a
// function because every posture it reports beyond the identity itself — the
// deployment posture, whether this caller may issue set-password links — is
// wiring the composition root injected onto Handlers, so passing them
// alongside would be a row of anonymous booleans at each call site.
func (h Handlers) meResponse(
	id Identity,
	sorMode crmcontracts.MeResponseSystemOfRecordMode,
) crmcontracts.MeResponse {
	adminPasswordLink := h.canIssuePasswordLink(id)
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
			Email:       openapi_types.Email(id.Email),
			DisplayName: id.DisplayName,
			Status:      "active",
		},
		Roles:         roles,
		Teams:         teams,
		WorkspaceName: id.WorkspaceName,
		SystemOfRecord: &struct {
			Mode crmcontracts.MeResponseSystemOfRecordMode `json:"mode"`
		}{Mode: sorMode},
		NonProduction:      h.nonProduction,
		DataResetAvailable: &h.dataResetAvailable,
		AdminPasswordLink:  adminPasswordLink,
		Authorization: &crmcontracts.Authorization{
			SeatType: contractSeatType(id.SeatType),
			Objects:  contractObjectGrants(id.Permissions.Objects),
		},
	}
}

// contractSeatType maps the stored seat onto the contract enum through the
// kernel's own predicate rather than a cast. Two properties follow that a cast
// would not give: the response can only ever carry a value the enum declares,
// and a seat the kernel does not recognize reports the ceiling that denies
// instead of the one that admits.
func contractSeatType(seat string) crmcontracts.AuthorizationSeatType {
	if principal.SeatType(seat).CanMutate() {
		return crmcontracts.AuthorizationSeatTypeFull
	}
	return crmcontracts.AuthorizationSeatTypeRead
}

// contractObjectGrants maps the merged permissions onto the wire shape.
//
// The field-by-field mapping is deliberate: principal.ObjectGrant carries no
// JSON tags, so handing it to the encoder would emit Create/Read/Update/Delete
// and every client check — which asks for the lowercase names the contract
// declares — would read absent and silently deny. That failure looks exactly
// like a correctly withheld permission, so it is worth the explicit copy.
func contractObjectGrants(objects map[string]principal.ObjectGrant) map[string]crmcontracts.RbacObjectGrant {
	grants := make(map[string]crmcontracts.RbacObjectGrant, len(objects))
	for object, grant := range objects {
		grants[object] = crmcontracts.RbacObjectGrant{
			Create: grant.Create,
			Read:   grant.Read,
			Update: grant.Update,
			Delete: grant.Delete,
		}
	}
	return grants
}
