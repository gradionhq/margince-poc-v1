// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package googleconn is the shared plumbing for the capture connectors that
// talk to Google over OAuth2 + REST (gmail, gcal): the authorized read-only
// GET with sentinel error mapping, and the OAuth code→refresh→access→owner
// Authenticate handshake with its persisted auth state. It is the Google
// analogue of capture/mailmap — extracted once the second concrete caller
// (gcal) appeared (ADR-0054 §3: grow a shared subpackage when a real second
// caller shows up, not for symmetry). It owns no provider specifics: each
// connector keeps the API surface, cursor shape, and extra error sentinels
// particular to it (Gmail's historyId / ErrHistoryGone, Calendar's syncToken /
// ErrSyncTokenGone).
package googleconn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
	"github.com/gradionhq/margince/backend/internal/shared/ports/mcp"
)

// httpTimeout bounds every Google call so a stalled request can't pin an API
// callback or the fleet-wide sync poller (http.DefaultClient has no timeout).
const httpTimeout = 30 * time.Second

// BoundedClient returns an HTTP client with the standard Google-call timeout.
func BoundedClient() *http.Client { return &http.Client{Timeout: httpTimeout} }

// ErrAuthRejected marks an OAuth/authorization failure Google reported (bad or
// expired code, revoked grant, missing scope). The transport maps it to a 422
// without echoing the raw provider error.
var ErrAuthRejected = errors.New("googleconn: the authorization was rejected")

// ErrUnreachable marks a transport-level failure reaching Google (DNS, TCP,
// TLS, timeout, 5xx). The transport maps it to a 502.
var ErrUnreachable = errors.New("googleconn: could not reach Google")

// Get performs an authorized GET against base+path and JSON-decodes the 200
// body into out. It returns the HTTP status (so a caller can special-case a
// provider code like 404/410) and maps a 401/403 to ErrAuthRejected and any
// other non-2xx/transport failure to ErrUnreachable. Google's raw body is never
// surfaced to the caller.
//
//craft:ignore naked-any out is the caller-supplied JSON decode target — its concrete type varies per endpoint
func Get(ctx context.Context, client *http.Client, base, accessToken, path string, q url.Values, out any) (int, error) {
	u := base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, fmt.Errorf("googleconn: building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("googleconn: %s: %w", path, ErrUnreachable)
	}
	//craft:ignore swallowed-errors best-effort close of the response body — the decoded result/status is what matters
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return resp.StatusCode, ErrAuthRejected
	}
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, ErrUnreachable
	}
	if err := json.Unmarshal(body, out); err != nil {
		return resp.StatusCode, fmt.Errorf("googleconn: decoding %s: %w", path, ErrUnreachable)
	}
	return resp.StatusCode, nil
}

// Descriptor is the shared static metadata for a read-only Google capture
// connector: read scope, green (read-only) tier, produces activities. name is
// the registry key ("gmail", "gcal"). The two Google connectors are identical
// here; a future one that isn't simply builds its own connector.Descriptor.
func Descriptor(name string) connector.Descriptor {
	return connector.Descriptor{
		Name:     name,
		Version:  "1",
		Scopes:   []principal.Scope{principal.ScopeRead},
		RiskTier: mcp.TierGreen, // read-only capture
		Produces: []datasource.EntityType{datasource.EntityActivity},
	}
}

// Session opens one sync/health pass: it unseals the AuthState and mints a fresh
// access token from the durable refresh token, returning the connected owner
// (the internal-vs-external anchor) and the short-lived access token. A stored
// bundle we cannot read is a corruption, surfaced as an error rather than
// silently treated as a fresh connection.
func Session(ctx context.Context, oauth OAuth, auth connector.Auth) (owner, accessToken string, err error) {
	var st AuthState
	if err := json.Unmarshal(auth, &st); err != nil {
		return "", "", fmt.Errorf("googleconn: malformed auth state: %w", err)
	}
	access, err := oauth.AccessToken(ctx, st.RefreshToken)
	if err != nil {
		return "", "", err
	}
	return st.Owner, access, nil
}

// OAuth is the OAuth2 handshake surface each Google connector supplies to
// Authenticate — the same three-method shape gmail and gcal implement.
type OAuth interface {
	AuthCodeURL(state, redirectURI string) string
	Exchange(ctx context.Context, code, redirectURI string) (refreshToken string, err error)
	AccessToken(ctx context.Context, refreshToken string) (accessToken string, err error)
}

// AuthState is the persisted credential bundle (the opaque connector.Auth). The
// refresh token is the durable secret; the short-lived access token is re-minted
// from it each Sync and never stored. Owner is the connected account's address —
// the internal-vs-external anchor.
type AuthState struct {
	RefreshToken string   `json:"refresh_token"`
	Owner        string   `json:"owner_email"`
	Scopes       []string `json:"scopes"`
}

// authPayload is the connect request the transport hands to Authenticate: the
// OAuth authorization code and the redirect URI it was issued against.
type authPayload struct {
	Code        string `json:"code"`
	RedirectURI string `json:"redirect_uri"`
}

// AuthRequestFrom packages an OAuth callback's code into the opaque connector
// AuthRequest the callback handler passes to Authenticate.
func AuthRequestFrom(code, redirectURI string) (connector.AuthRequest, error) {
	payload, err := json.Marshal(authPayload{Code: code, RedirectURI: redirectURI})
	if err != nil {
		return connector.AuthRequest{}, fmt.Errorf("googleconn: encoding auth payload: %w", err)
	}
	return connector.AuthRequest{Payload: payload}, nil
}

// ScopeStrings renders principal scopes as the plain strings the AuthState carries.
func ScopeStrings(scopes []principal.Scope) []string {
	out := make([]string, 0, len(scopes))
	for _, s := range scopes {
		out = append(out, string(s))
	}
	return out
}

// OwnerResolver turns a fresh access token into the connected account's address
// — Gmail's profile emailAddress, Calendar's primary-calendar id. It is the one
// provider-specific step in the otherwise-shared Authenticate handshake.
type OwnerResolver func(ctx context.Context, accessToken string) (string, error)

// Authenticate runs the shared OAuth code→refresh→access→owner handshake and
// returns the sealed AuthState as the opaque connector.Auth. scopes are the
// connector's declared scopes, frozen into the bundle; resolveOwner is the
// per-connector call that names the account. The access token is discarded —
// only the durable refresh token persists.
func Authenticate(ctx context.Context, oauth OAuth, req connector.AuthRequest, scopes []principal.Scope, resolveOwner OwnerResolver) (connector.Auth, error) {
	var p authPayload
	if err := json.Unmarshal(req.Payload, &p); err != nil {
		return nil, fmt.Errorf("googleconn: malformed auth payload: %w", err)
	}
	if p.Code == "" {
		return nil, fmt.Errorf("googleconn: authorization code required: %w", ErrAuthRejected)
	}
	refresh, err := oauth.Exchange(ctx, p.Code, p.RedirectURI)
	if err != nil {
		return nil, err
	}
	access, err := oauth.AccessToken(ctx, refresh)
	if err != nil {
		return nil, err
	}
	owner, err := resolveOwner(ctx, access)
	if err != nil {
		return nil, err
	}
	state := AuthState{RefreshToken: refresh, Owner: owner, Scopes: ScopeStrings(scopes)}
	//nolint:gosec // G117: sealing the connector's own refresh token into the opaque Auth bundle IS the intended path — the registry stores it encrypted in the vault, never logged or returned
	auth, err := json.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("googleconn: encoding auth state: %w", err)
	}
	return auth, nil
}
