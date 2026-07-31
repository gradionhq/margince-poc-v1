// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package identity

// Federated human sign-in (A107/ADR-0061 §6) — the transport half: the two
// browser navigations that bracket the provider round-trip.
//
// Both answer 302 and nothing else. A human who is mid-sign-in is in a
// browser, not an API client: a JSON problem document would be a dead end,
// so every failure returns to the login screen carrying one bounded
// `sso_error` code and the screen says what to do next. The codes are
// deliberately coarse — `not_linked` covers both "no such user" and
// "already bound elsewhere", because telling those apart would confirm
// which addresses exist.

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/identity/internal/oidc"
	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
)

// oidcStateCookie carries the raw state back from the provider. Two
// properties make it the browser binding: SameSite=Lax, so it survives the
// provider's cross-site top-level redirect where the Strict session cookie
// would not, and a path scoped to the federated endpoints, so it is never
// sent anywhere else.
const oidcStateCookie = "crm_oidc"

// oidcCookiePath scopes the state cookie to the flow that uses it.
const oidcCookiePath = "/v1/auth/oidc"

// The bounded `sso_error` vocabulary the login screen renders (contract:
// completeOidcLogin). Every failure maps onto exactly one of these.
const (
	ssoErrorDenied              = "denied"
	ssoErrorExpired             = "expired"
	ssoErrorRejected            = "rejected"
	ssoErrorUnverifiedEmail     = "unverified_email"
	ssoErrorDomainNotAllowed    = "domain_not_allowed"
	ssoErrorNotLinked           = "not_linked"
	ssoErrorProviderUnavailable = "provider_unavailable"
)

// OIDCLoginConfig is what the composition root hands the identity surface
// to make federated sign-in real. Absent it, the endpoints answer 404 and
// the capabilities probe lists no provider — the login screen then draws no
// button, which is the A107 §10.2 honesty rule.
type OIDCLoginConfig struct {
	// ProviderKey is the capability key and the `{provider}` path segment.
	ProviderKey string
	// Label is the button copy the capabilities probe serves.
	Label string

	Issuer       string
	ClientID     string
	ClientSecret string
	// RedirectURI must equal the value registered at the provider, exactly.
	RedirectURI string
	// AppBaseURL is where the browser lands afterwards — the SPA's canonical
	// base. Empty means same-origin, and the redirect stays a bare path.
	AppBaseURL string
	// AllowedDomains optionally restricts which verified email domains may
	// sign in. Empty means no domain restriction (the flow still binds only
	// to an existing local human).
	AllowedDomains []string
}

// OIDCLogin is a wired federated sign-in provider: the relying-party
// mechanics plus the deployment's own policy around them. The composition
// root builds it once at boot, so a misconfigured provider fails the boot
// rather than becoming a button that dead-ends the first human to click it.
type OIDCLogin struct {
	key            string
	label          string
	appBaseURL     string
	allowedDomains []string
	provider       *oidc.Provider
}

// NewOIDCLogin builds the provider from deployment configuration.
func NewOIDCLogin(cfg OIDCLoginConfig) (*OIDCLogin, error) {
	if cfg.ProviderKey == "" {
		return nil, errors.New("identity: federated sign-in needs a provider key")
	}
	if cfg.Label == "" {
		return nil, errors.New("identity: federated sign-in needs a button label — the login screen renders it verbatim")
	}
	provider, err := oidc.New(oidc.Config{
		Issuer:       cfg.Issuer,
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		RedirectURI:  cfg.RedirectURI,
	})
	if err != nil {
		return nil, err
	}
	domains := make([]string, 0, len(cfg.AllowedDomains))
	for _, domain := range cfg.AllowedDomains {
		// A blank entry is dropped rather than kept as "": it would make the
		// single-domain hint send `hd=` and, worse, could not match any real
		// claim, so one stray line would refuse every account.
		if normalized := strings.ToLower(strings.TrimSpace(domain)); normalized != "" {
			domains = append(domains, normalized)
		}
	}
	return &OIDCLogin{
		key:            cfg.ProviderKey,
		label:          cfg.Label,
		appBaseURL:     strings.TrimRight(cfg.AppBaseURL, "/"),
		allowedDomains: domains,
		provider:       provider,
	}, nil
}

// WithOIDCLogin wires federated sign-in onto the identity surface. Without
// it the two OIDC endpoints answer 404 and the capabilities probe lists no
// provider, so the login screen draws no button.
func (h Handlers) WithOIDCLogin(login *OIDCLogin) Handlers {
	h.oidc = login
	return h
}

// wiredOIDC resolves the request's provider segment against the configured
// one. A key this installation did not configure is 404: an unconfigured
// provider discloses nothing beyond what /auth/capabilities already says.
func (h Handlers) wiredOIDC(provider crmcontracts.OidcProvider) (*OIDCLogin, bool) {
	if h.oidc == nil || h.oidc.key != string(provider) {
		return nil, false
	}
	return h.oidc, true
}

// StartOidcLogin implements (GET /auth/oidc/{provider}/start): mint the
// attempt, bind it to this browser, and hand the human to the provider.
func (h Handlers) StartOidcLogin(w http.ResponseWriter, r *http.Request, provider crmcontracts.OidcProvider) {
	login, ok := h.wiredOIDC(provider)
	if !ok {
		httperr.Write(w, r, apperrors.ErrNotFound)
		return
	}
	// Each attempt costs a provider round-trip and a database row, so the
	// endpoint carries the same per-IP ceiling as the other pre-auth calls.
	if !h.oidcPerIP.Allow(clientIP(r)) {
		httperr.Write(w, r, apperrors.ErrBudgetExceeded)
		return
	}

	attempt, err := oidc.NewAuthRequest()
	if err != nil {
		slog.ErrorContext(r.Context(), "federated sign-in could not mint its secrets", "provider", login.key, "err", err)
		redirectToLogin(w, r, login, ssoErrorProviderUnavailable)
		return
	}
	// The authorization URL is built BEFORE the state is stored: discovery
	// runs inside it, so an unreachable provider is caught while the human
	// is still on the login screen rather than after a redirect out.
	authURL, err := login.provider.AuthCodeURL(r.Context(), attempt, login.hostedDomainHint())
	if err != nil {
		slog.ErrorContext(r.Context(), "federated sign-in could not start", "provider", login.key, "err", err)
		redirectToLogin(w, r, login, ssoErrorProviderUnavailable)
		return
	}
	if err := h.svc.StartOIDCLogin(r.Context(), login.key, attempt); err != nil {
		slog.ErrorContext(r.Context(), "federated sign-in could not store its login state", "provider", login.key, "err", err)
		redirectToLogin(w, r, login, ssoErrorProviderUnavailable)
		return
	}

	setOIDCStateCookie(w, attempt.State)
	http.Redirect(w, r, authURL, http.StatusFound)
}

// CompleteOidcLogin implements (GET /auth/oidc/{provider}/callback): claim
// the state, redeem the code server-side, validate the ID token in full,
// and open a session for the human it proves.
func (h Handlers) CompleteOidcLogin(w http.ResponseWriter, r *http.Request, provider crmcontracts.OidcProvider, params crmcontracts.CompleteOidcLoginParams) {
	login, ok := h.wiredOIDC(provider)
	if !ok {
		httperr.Write(w, r, apperrors.ErrNotFound)
		return
	}
	// The callback carries the same per-IP ceiling as the start endpoint. It
	// does real work for an unauthenticated caller — a database claim and, past
	// it, a provider round-trip — and the throttle is what keeps a loop against
	// it from being an amplifier. Refused as a redirect like everything else
	// here, so a human caught behind a noisy network sees the login screen.
	if !h.oidcPerIP.Allow(clientIP(r)) {
		redirectToLogin(w, r, login, ssoErrorProviderUnavailable)
		return
	}
	// The state cookie is spent whatever happens next: it belongs to exactly
	// one attempt, and leaving it set would keep a dead handle in the browser.
	clearOIDCStateCookie(w)

	// From here the answer is a redirect no matter what — including on an
	// infrastructure failure. A human mid-navigation has no use for a problem
	// document, and the contract says 302: `provider_unavailable` is what a
	// database we could not reach looks like from the login screen, and the
	// operator's log carries the real reason.
	attempt, refusal, err := h.claimAttempt(r, login, params)
	if err != nil {
		slog.ErrorContext(r.Context(), "federated sign-in could not claim its login state", "provider", login.key, "err", err)
		redirectToLogin(w, r, login, ssoErrorProviderUnavailable)
		return
	}
	if refusal != "" {
		redirectToLogin(w, r, login, refusal)
		return
	}

	external, err := login.provider.ExchangeAndVerify(r.Context(), *params.Code, attempt.Verifier, attempt.Nonce)
	if err != nil {
		redirectToLogin(w, r, login, exchangeFailureCode(r.Context(), login.key, err))
		return
	}
	if !login.domainAllowed(external) {
		slog.WarnContext(r.Context(), "federated sign-in refused: domain not allowed", "provider", login.key, "hosted_domain", external.HostedDomain)
		redirectToLogin(w, r, login, ssoErrorDomainNotAllowed)
		return
	}

	// The session cookie is the whole result: the SPA reads its identity from
	// /me exactly as it does after a password sign-in.
	token, err := h.svc.CompleteOIDCLogin(r.Context(), external)
	switch {
	case errors.Is(err, errNoLinkableUser), errors.Is(err, errUserBoundElsewhere):
		// The distinction stays in the operator's system_log — the answer to
		// the browser is one neutral code either way.
		slog.WarnContext(r.Context(), "federated sign-in matched no linkable user", "provider", login.key, "err", err)
		redirectToLogin(w, r, login, ssoErrorNotLinked)
		return
	case err != nil:
		slog.ErrorContext(r.Context(), "federated sign-in could not open a session", "provider", login.key, "err", err)
		redirectToLogin(w, r, login, ssoErrorProviderUnavailable)
		return
	}

	setSessionCookie(w, token)
	http.Redirect(w, r, login.appURL(""), http.StatusFound)
}

// claimAttempt validates everything the returning browser brought with it
// and consumes the login state. It returns either the attempt's secrets, or
// the bounded refusal code the login screen should show — a non-empty
// refusal means the flow stops here. Only an infrastructure failure comes
// back as an error.
func (h Handlers) claimAttempt(r *http.Request, login *OIDCLogin, params crmcontracts.CompleteOidcLoginParams) (oidcAttempt, string, error) {
	if params.Error != nil && *params.Error != "" {
		// The provider names the refusal (access_denied, consent_required, …).
		// The human's own cancellation is the overwhelmingly common case and
		// the copy for it is not an error message.
		//
		// The reason is bounded before it is logged: this parameter arrives on
		// an unauthenticated URL and is whatever the caller typed, so logging
		// it verbatim would let anyone choose how many bytes of their own text
		// land in the operator's log.
		slog.InfoContext(r.Context(), "federated sign-in refused at the provider",
			"provider", login.key, "reason", oidc.SafeReason(*params.Error))
		return oidcAttempt{}, ssoErrorDenied, nil
	}
	if !stateCookieMatches(r, params.State) {
		// Either the attempt is older than its cookie, or the callback did
		// not originate from a sign-in this browser started. One answer for
		// both: restart the flow. Checked BEFORE the database, so a forged
		// callback cannot spend somebody's real attempt.
		return oidcAttempt{}, ssoErrorExpired, nil
	}
	attempt, err := h.svc.ClaimOIDCLoginState(r.Context(), login.key, *params.State)
	if errors.Is(err, apperrors.ErrNotFound) {
		return oidcAttempt{}, ssoErrorExpired, nil
	}
	if err != nil {
		return oidcAttempt{}, "", err
	}
	if params.Code == nil || *params.Code == "" {
		// A callback with neither error nor code is not something a human can
		// act on; the state is spent, so the only honest move is to restart.
		return oidcAttempt{}, ssoErrorRejected, nil
	}
	return attempt, "", nil
}

// stateCookieMatches is the browser binding: the returning callback's state
// must equal the one this browser was given. A missing cookie, a missing
// parameter, and a mismatch are all the same answer — no.
func stateCookieMatches(r *http.Request, state *string) bool {
	cookie, err := r.Cookie(oidcStateCookie)
	if err != nil || state == nil || *state == "" {
		return false
	}
	return cookie.Value != "" && cookie.Value == *state
}

// exchangeFailureCode classifies a failed exchange or validation. The
// provider's own detail is logged for the operator and never rendered: a
// human cannot act on "aud mismatch", and the detail can name internals.
func exchangeFailureCode(ctx context.Context, providerKey string, err error) string {
	switch {
	case errors.Is(err, oidc.ErrProviderUnavailable):
		slog.ErrorContext(ctx, "federated sign-in: provider unavailable", "provider", providerKey, "err", err)
		return ssoErrorProviderUnavailable
	case errors.Is(err, oidc.ErrEmailUnverified):
		slog.WarnContext(ctx, "federated sign-in: provider has not verified the email", "provider", providerKey)
		return ssoErrorUnverifiedEmail
	default:
		// Both a refused exchange and a token that fails validation land
		// here: from the human's side they are the same event — the provider
		// did not produce a usable proof of identity.
		slog.WarnContext(ctx, "federated sign-in: authorization not usable", "provider", providerKey, "err", err)
		return ssoErrorRejected
	}
}

// domainAllowed applies the operator's domain restriction. The provider's
// own `hd` (hosted-domain) claim is preferred where present — §14 requires
// the provider's verified organization claim over string parsing — and the
// verified email's domain is the fallback for providers that send none.
func (l *OIDCLogin) domainAllowed(external oidc.Identity) bool {
	if len(l.allowedDomains) == 0 {
		return true
	}
	claimed := external.HostedDomain
	if claimed == "" {
		_, domain, found := strings.Cut(external.Email, "@")
		if !found {
			return false
		}
		claimed = domain
	}
	// Lowered HERE as well as at the claim, so the comparison holds whatever a
	// provider returns and does not depend on a normalization two files away.
	claimed = strings.ToLower(claimed)
	for _, allowed := range l.allowedDomains {
		if claimed == allowed {
			return true
		}
	}
	return false
}

// hostedDomainHint narrows the provider's account chooser when the
// installation accepts exactly one domain. With several, hinting one would
// steer a human away from an account that is equally allowed.
func (l *OIDCLogin) hostedDomainHint() string {
	if len(l.allowedDomains) == 1 {
		return l.allowedDomains[0]
	}
	return ""
}

// redirectToLogin returns the human to the login screen with one bounded
// code. Its 302 is the same shape as the success redirect — a failed
// federated sign-in is a state of the login screen, not an API error.
func redirectToLogin(w http.ResponseWriter, r *http.Request, login *OIDCLogin, code string) {
	// #nosec G710 -- not attacker-steerable: the base is the operator's configured
	// AppBaseURL and the only variable part is one of the fixed ssoError* constants,
	// query-escaped. Nothing from the request reaches this URL.
	http.Redirect(w, r, login.loginURL(code), http.StatusFound)
}

// loginURL is the login screen carrying an sso_error code.
func (l *OIDCLogin) loginURL(code string) string {
	return l.appURL("?sso_error=" + url.QueryEscape(code))
}

// appURL builds an in-app target. The base is the operator's configured SPA
// origin, or a bare path when the app and the API share one — either way it
// is derived from configuration and never from the request, so this
// redirect cannot be steered by a query parameter.
func (l *OIDCLogin) appURL(query string) string {
	return l.appBaseURL + "/" + query
}

func setOIDCStateCookie(w http.ResponseWriter, state string) {
	http.SetCookie(w, &http.Cookie{
		Name: oidcStateCookie, Value: state,
		Path: oidcCookiePath, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
		MaxAge: int(oidcStateTTL.Seconds()),
	})
}

func clearOIDCStateCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: oidcStateCookie, Value: "", MaxAge: -1,
		Path: oidcCookiePath, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
}
