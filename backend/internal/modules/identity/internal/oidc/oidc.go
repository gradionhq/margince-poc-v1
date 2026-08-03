// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package oidc is the OpenID Connect relying-party mechanics behind
// federated human sign-in (A107/ADR-0061 §6, §10.3): provider discovery,
// the authorization-code request with PKCE S256, the server-side code
// exchange, and full ID-token validation.
//
// It is deliberately NOT the connector OAuth flow (capture/oauthflow):
// that one authorizes access to a mailbox and never asserts who is at the
// keyboard. Here the ID token IS the assertion, so everything this package
// exists to do is refuse a token that does not prove it — signature,
// issuer, audience, expiry, nonce, and a provider-verified email.
//
// The package holds no database and no session concept. It answers one
// question — "which provider identity is this, if any?" — and the identity
// module decides which local human that may become.
package oidc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// httpTimeout bounds every provider round-trip (discovery, JWKS, token). A
// wedged identity provider must fail the sign-in, not hang the request.
const httpTimeout = 15 * time.Second

// The three failure classes a caller has to tell apart, because each one
// gets a different answer on the login screen: the provider (or the
// network) is having a bad day, the authorization itself was refused, or
// the response arrived but does not prove an identity.
var (
	// ErrProviderUnavailable is weather: DNS, TLS, timeouts, a 5xx, an
	// undecodable discovery document. Retrying may work.
	ErrProviderUnavailable = errors.New("oidc: provider unavailable")
	// ErrAuthorizationRejected is the provider refusing this exchange — a
	// stale or replayed code, a mismatched verifier, a wrong client
	// credential. Retrying the same code never works.
	ErrAuthorizationRejected = errors.New("oidc: authorization rejected")
	// ErrTokenInvalid means the ID token failed validation. It is a
	// security refusal, and the only honest answer is to refuse the login.
	ErrTokenInvalid = errors.New("oidc: id token invalid")
	// ErrEmailUnverified is the ONE validation failure a human can act on
	// themselves — the provider has not verified the address, so they must
	// verify it there first. Separate from ErrTokenInvalid because the login
	// screen can give real advice for it instead of a generic refusal.
	ErrEmailUnverified = errors.New("oidc: provider has not verified the email address")
)

// Config wires the relying party. Issuer is the discovery origin, not an
// endpoint set: the authorization, token, and JWKS URLs are read from the
// provider's own document, so a provider that moves them keeps working.
type Config struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	// RedirectURI must match the provider's registered value EXACTLY; it is
	// sent on both the authorization request and the code exchange, where
	// the provider re-checks it.
	RedirectURI string

	// HTTPClient overrides the bounded default (tests point the issuer at
	// an httptest server and inject its client).
	HTTPClient *http.Client
	// Now is the clock every expiry is judged against, injected so the
	// token-validation tests prove the exp/iat boundaries without sleeping.
	Now func() time.Time
}

// Provider is a configured relying party. It caches the discovery document
// and the signing keys per the provider's own cache headers, so a login
// costs one token round-trip rather than three. Safe for concurrent use.
type Provider struct {
	cfg  Config
	http *http.Client
	now  func() time.Time

	// mu guards both caches. A mutex rather than an RWMutex on purpose: the
	// contended path is a cache MISS, which must fetch exactly once, and
	// the hit is a map read too cheap to be worth a second lock mode.
	mu       sync.Mutex
	doc      *discoveryDocument
	docUntil time.Time
	// failedUntil is the negative cache: a provider that just failed
	// discovery is not dialled again before this instant.
	failedUntil time.Time
	keys        keySet
}

// New builds the relying party for one issuer. It performs no I/O —
// discovery happens on first use, so a provider that is briefly down at
// boot does not prevent the installation from starting (the login screen
// simply reports the provider as failing when someone tries it).
func New(cfg Config) (*Provider, error) {
	if cfg.Issuer == "" || cfg.ClientID == "" || cfg.ClientSecret == "" || cfg.RedirectURI == "" {
		return nil, errors.New("oidc: issuer, client id, client secret and redirect uri are all required")
	}
	if !strings.HasPrefix(cfg.Issuer, "https://") && !isLoopbackIssuer(cfg.Issuer) {
		// HTTPS outside explicitly permitted local development (§10.3): an
		// ID token fetched over plain http is an unauthenticated assertion.
		return nil, fmt.Errorf("oidc: issuer %q must be https (http is permitted only for a loopback test issuer)", cfg.Issuer)
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: httpTimeout}
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Provider{cfg: cfg, http: client, now: now}, nil
}

// isLoopbackIssuer permits a plain-http issuer ONLY at loopback, which is
// where the integration lane's fake issuer lives. A deployment cannot
// reach a loopback issuer of someone else's machine, so this cannot become
// a production downgrade.
func isLoopbackIssuer(issuer string) bool {
	u, err := url.Parse(issuer)
	if err != nil || u.Scheme != "http" {
		return false
	}
	// Hostname() strips the brackets an IPv6 authority carries, so the
	// comparison is against the bare address.
	return u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1"
}

// Issuer is the configured discovery origin — the value stored alongside
// the subject in a permanent binding.
func (p *Provider) Issuer() string { return p.cfg.Issuer }

// AuthRequest is one outbound authorization attempt's secrets. The caller
// persists them against the state and hands the verifier back at the
// callback; none of them may ever be inferred from the returning request.
type AuthRequest struct {
	// State is the raw handle: it travels to the provider and back, and its
	// hash is what the caller stores.
	State string
	// Nonce is echoed inside the ID token, which is what makes it a replay
	// defence rather than a second state.
	Nonce string
	// Verifier is the PKCE secret; only its S256 challenge goes to the
	// provider.
	Verifier string
}

// NewAuthRequest mints the per-attempt state, nonce, and PKCE verifier.
func NewAuthRequest() (AuthRequest, error) {
	secrets := make([]string, 3)
	for i := range secrets {
		// 32 bytes each: comfortably past the 128-bit entropy floor A107
		// §9.1 sets for a single-use credential, and inside the RFC 7636
		// §4.1 verifier length range.
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			return AuthRequest{}, fmt.Errorf("oidc: generating authorization secrets: %w", err)
		}
		secrets[i] = base64.RawURLEncoding.EncodeToString(buf)
	}
	return AuthRequest{State: secrets[0], Nonce: secrets[1], Verifier: secrets[2]}, nil
}

// challenge is the RFC 7636 S256 transformation of a verifier.
func challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// AuthCodeURL builds the authorization request the browser is redirected
// to. Discovery runs here, so an unreachable provider is caught BEFORE the
// human leaves the login screen rather than on the way back.
func (p *Provider) AuthCodeURL(ctx context.Context, req AuthRequest, hostedDomainHint string) (string, error) {
	doc, err := p.discover(ctx)
	if err != nil {
		return "", err
	}
	q := url.Values{
		"client_id":             {p.cfg.ClientID},
		"redirect_uri":          {p.cfg.RedirectURI},
		"response_type":         {"code"},
		"scope":                 {"openid email profile"},
		"state":                 {req.State},
		"nonce":                 {req.Nonce},
		"code_challenge":        {challenge(req.Verifier)},
		"code_challenge_method": {"S256"},
	}
	if hostedDomainHint != "" {
		// A hint only: `hd` narrows the provider's account chooser, and the
		// domain restriction is still enforced here against the token's own
		// claims. Trusting it as the enforcement point would be trusting a
		// parameter the browser could have rewritten.
		q.Set("hd", hostedDomainHint)
	}
	// Merged through url.Parse rather than concatenated: an endpoint that
	// already carries a query keeps it, and one carrying a fragment cannot
	// swallow the whole authorization request into a browser-only fragment.
	endpoint, err := url.Parse(doc.AuthorizationEndpoint)
	if err != nil {
		return "", fmt.Errorf("%w: authorization endpoint is not a URL", ErrProviderUnavailable)
	}
	existing := endpoint.Query()
	for key, values := range q {
		existing[key] = values
	}
	endpoint.RawQuery = existing.Encode()
	return endpoint.String(), nil
}
