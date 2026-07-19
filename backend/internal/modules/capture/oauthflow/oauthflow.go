// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package oauthflow is the OAuth2 authorization-code + refresh handshake
// shared by the OAuth mail connectors (gmail, graph). The flow is identical
// across providers — build a consent URL, exchange the code for a refresh
// token, mint short-lived access tokens — so it lives once here; each
// connector supplies only what genuinely differs: the endpoints, the
// provider-specific consent parameters, whether the token forms carry the
// scope, and its own error sentinels (returned verbatim so the connector's
// registry classification and log identity are preserved).
package oauthflow

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// httpTimeout bounds every token-endpoint call; a wedged IdP must not hang a
// sync.
const httpTimeout = 30 * time.Second

// paramClientID is the OAuth2 client-id form/query key, used in the consent
// URL and both token forms.
const paramClientID = "client_id"

// Config wires the handshake for one provider. AuthRejected and Unreachable
// are the connector's own sentinels — the flow returns them verbatim, never
// its own, so callers keep classifying failures exactly as before.
type Config struct {
	Provider     string // "gmail" / "graph" — names the provider in error detail
	ClientID     string
	ClientSecret string
	Scopes       []string
	AuthURL      string
	TokenURL     string

	// AuthParams are the provider-specific consent-URL query parameters
	// (Google: access_type/prompt/include_granted_scopes; Microsoft:
	// response_mode) merged over the common set.
	AuthParams map[string]string
	// ScopeInTokenForms adds the space-joined scope to the exchange and
	// refresh forms — Microsoft requires it, Google forbids it.
	ScopeInTokenForms bool

	// AuthRejected wraps connector.ErrAuthRejected; Unreachable wraps
	// connector.ErrUnreachable. Both are required.
	AuthRejected error
	Unreachable  error

	// HTTPClient overrides the bounded default (tests inject none and set
	// TokenURL to an httptest server instead).
	HTTPClient *http.Client
}

// Client runs the handshake for one configured provider.
type Client struct {
	http *http.Client
	cfg  Config
}

// New builds the flow client. The caller has already resolved the
// endpoints (each connector defaults them per provider before calling).
func New(cfg Config) *Client {
	c := cfg.HTTPClient
	if c == nil {
		c = &http.Client{Timeout: httpTimeout}
	}
	return &Client{http: c, cfg: cfg}
}

// AuthCodeURL builds the consent URL: the common authorization-code
// parameters plus the provider's own, all under the configured scope.
func (c *Client) AuthCodeURL(state, redirectURI string) string {
	q := url.Values{
		paramClientID:   {c.cfg.ClientID},
		"redirect_uri":  {redirectURI},
		"response_type": {"code"},
		"scope":         {strings.Join(c.cfg.Scopes, " ")},
		"state":         {state},
	}
	for k, v := range c.cfg.AuthParams {
		q.Set(k, v)
	}
	return c.cfg.AuthURL + "?" + q.Encode()
}

// tokenResponse is the subset of the token endpoint payload both providers
// return that this flow reads.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

// Exchange redeems the authorization code for a durable refresh token.
// A consent that returns no refresh token did not grant offline access —
// the connector cannot sync later, so it is a rejected authorization.
func (c *Client) Exchange(ctx context.Context, code, redirectURI string) (string, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		paramClientID:   {c.cfg.ClientID},
		"client_secret": {c.cfg.ClientSecret},
	}
	c.addScope(form)
	tok, err := c.token(ctx, form)
	if err != nil {
		return "", err
	}
	if tok.RefreshToken == "" {
		return "", fmt.Errorf("%s: consent returned no refresh token: %w", c.cfg.Provider, c.cfg.AuthRejected)
	}
	return tok.RefreshToken, nil
}

// AccessToken redeems the stored refresh token for a short-lived access
// token. A provider that rotates the refresh token on redemption (Microsoft)
// leaves the stored one valid for its own lifetime, so the rotation need not
// be persisted here.
func (c *Client) AccessToken(ctx context.Context, refreshToken string) (string, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		paramClientID:   {c.cfg.ClientID},
		"client_secret": {c.cfg.ClientSecret},
	}
	c.addScope(form)
	tok, err := c.token(ctx, form)
	if err != nil {
		return "", err
	}
	if tok.AccessToken == "" {
		return "", fmt.Errorf("%s: token refresh returned no access token: %w", c.cfg.Provider, c.cfg.AuthRejected)
	}
	return tok.AccessToken, nil
}

func (c *Client) addScope(form url.Values) {
	if c.cfg.ScopeInTokenForms {
		form.Set("scope", strings.Join(c.cfg.Scopes, " "))
	}
}

// token posts the form to the token endpoint and decodes the response. A 4xx
// is an authorization problem (AuthRejected); anything else reaching or
// reading the endpoint is Unreachable. The provider's raw body never reaches
// the caller.
func (c *Client) token(ctx context.Context, form url.Values) (tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, fmt.Errorf("%s: building token request: %w", c.cfg.Provider, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("%s: token endpoint: %w", c.cfg.Provider, c.cfg.Unreachable)
	}
	//craft:ignore swallowed-errors best-effort close of the token response body — the exchange result is already read below
	defer func() { _ = resp.Body.Close() }()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	// A throttled token endpoint is weather, not a bad credential: honor
	// Retry-After and let the registry back off, rather than parking the
	// connection as rejected. Classified on status before the body matters.
	if resp.StatusCode == http.StatusTooManyRequests {
		return tokenResponse{}, &connector.RateLimitedError{RetryAfter: retryAfter(resp)}
	}
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		return tokenResponse{}, c.cfg.AuthRejected
	}
	if resp.StatusCode != http.StatusOK {
		return tokenResponse{}, c.cfg.Unreachable
	}
	if readErr != nil {
		// A truncated body that happens to be valid-JSON prefix must never
		// pass as a complete token response.
		return tokenResponse{}, fmt.Errorf("%s: reading token response: %w", c.cfg.Provider, c.cfg.Unreachable)
	}
	var tok tokenResponse
	if err := json.Unmarshal(body, &tok); err != nil {
		return tokenResponse{}, fmt.Errorf("%s: decoding token response: %w", c.cfg.Provider, c.cfg.Unreachable)
	}
	return tok, nil
}

// retryAfter parses the token endpoint's Retry-After (delta-seconds); zero
// when absent, leaving the registry's own backoff to take over.
func retryAfter(resp *http.Response) time.Duration {
	if s := resp.Header.Get("Retry-After"); s != "" {
		if secs, err := strconv.Atoi(s); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return 0
}
