// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package oidc

// The server-side code exchange. The authorization code arrives through the
// browser and is worth nothing on its own: it is redeemed here, over the
// back channel, authenticated by the client secret and bound to this
// browser by the PKCE verifier.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ExchangeAndVerify redeems the authorization code and returns the identity
// its ID token proves. The two steps are ONE operation on purpose: an
// access token this relying party never uses is not a result worth handing
// out, and a code exchange whose ID token then fails validation must leave
// the caller with nothing.
func (p *Provider) ExchangeAndVerify(ctx context.Context, code, verifier, expectedNonce string) (Identity, error) {
	doc, err := p.discover(ctx)
	if err != nil {
		return Identity{}, err
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {p.cfg.RedirectURI},
		"client_id":     {p.cfg.ClientID},
		"client_secret": {p.cfg.ClientSecret},
		"code_verifier": {verifier},
	}
	idToken, err := p.postToken(ctx, doc.TokenEndpoint, form)
	if err != nil {
		return Identity{}, err
	}
	return p.VerifyIDToken(ctx, idToken, expectedNonce)
}

// tokenResponse is the subset of the token endpoint's payload this relying
// party reads. access_token and refresh_token are deliberately absent: this
// flow authenticates a human and calls no provider API afterwards, so
// holding either would be storing a credential nothing needs.
type tokenResponse struct {
	IDToken string `json:"id_token"`
	Error   string `json:"error"`
}

// postToken performs the exchange. A 4xx is the provider refusing the
// authorization (stale code, replayed code, mismatched verifier, wrong
// client credential); anything else is the provider being unavailable. The
// provider's raw body never reaches the caller — only the RFC 6749 §5.2
// error code, which is a closed vocabulary and safe in an operator log.
func (p *Provider) postToken(ctx context.Context, endpoint string, form url.Values) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("oidc: building token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := p.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: token endpoint unreachable", ErrProviderUnavailable)
	}
	//craft:ignore swallowed-errors best-effort close of a fully-read response body; the exchange outcome is decided below
	defer func() { _ = resp.Body.Close() }()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		return "", fmt.Errorf("%w: token endpoint answered %d (%s)", ErrAuthorizationRejected, resp.StatusCode, oauthErrorCode(body))
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: token endpoint answered %d", ErrProviderUnavailable, resp.StatusCode)
	}
	if readErr != nil {
		// A truncated body whose prefix happens to be valid JSON must never
		// pass as a complete token response.
		return "", fmt.Errorf("%w: reading token response", ErrProviderUnavailable)
	}
	var parsed tokenResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("%w: token endpoint returned undecodable JSON", ErrProviderUnavailable)
	}
	if parsed.IDToken == "" {
		// A 200 with no id_token is not an authentication. Most often it
		// means the `openid` scope was dropped somewhere — a configuration
		// fault, so it reads as a rejected authorization rather than
		// transient weather a retry could clear.
		return "", fmt.Errorf("%w: token response carries no id_token", ErrAuthorizationRejected)
	}
	return parsed.IDToken, nil
}

// oauthErrorCode extracts the RFC 6749 §5.2 error code from a refusal body,
// "unspecified" when the body carries none or does not decode — an
// unparsable body must not masquerade as a named reason.
func oauthErrorCode(body []byte) string {
	var parsed tokenResponse
	if err := json.Unmarshal(body, &parsed); err != nil || parsed.Error == "" {
		return "unspecified"
	}
	// Bounded to a short, ASCII-safe slug: the field is provider-controlled
	// and this value lands in an operator log line.
	code := parsed.Error
	if len(code) > 64 {
		code = code[:64]
	}
	for _, r := range code {
		if r < 'a' || r > 'z' {
			if r != '_' && (r < 'A' || r > 'Z') {
				return "unspecified"
			}
		}
	}
	return code
}
