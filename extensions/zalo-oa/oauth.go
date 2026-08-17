// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalooa

// The browser round trip, and the token endpoint behind it.
//
// There is NO client-credentials grant for an Official Account. An OA admin
// opens a permission URL, reads what is being asked for, and clicks *Cho phép*;
// what comes back is a code that is single-use and dies in ten minutes, which
// is exchanged for a pair of tokens. Every part of this file exists because that
// human step cannot be automated away, and the design's job is to make the parts
// around it recoverable.
//
// The token endpoint answers a DIFFERENT shape from the OpenAPI host — a flat
// grant document, not the `{error, message, data}` envelope client.go reads —
// so it has its own transport here rather than sharing that one.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// permissionURL is where the OA admin goes to grant access.
	permissionURL = "https://oauth.zaloapp.com/v4/oa/permission"
	// tokenURL redeems a code and rotates a refresh token. Both grants post
	// here; only `grant_type` differs.
	//nolint:gosec // G101 false positive: the provider's public token ENDPOINT, which is the opposite of a secret
	tokenURL = "https://oauth.zaloapp.com/v4/oa/access_token"
)

const (
	// verifierLength is fixed at 43 characters because Zalo's documentation
	// specifies exactly that, not a range.
	verifierLength = 43

	// verifierAlphabet is the character set Zalo requires: upper, lower and
	// digits. It is NARROWER than RFC 7636, which also admits `-._~` — this
	// follows the provider's rule rather than the RFC's, because the provider is
	// what validates it.
	verifierAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
)

// newVerifier draws a PKCE code verifier from crypto/rand.
func newVerifier() (string, error) {
	out := make([]byte, verifierLength)
	limit := big.NewInt(int64(len(verifierAlphabet)))
	for i := range out {
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", fmt.Errorf("zalo-oa: drawing a code verifier: %w", err)
		}
		out[i] = verifierAlphabet[n.Int64()]
	}
	return string(out), nil
}

// challengeFor derives the PKCE code challenge from a verifier.
//
// THE ENCODING IS THE ONE GENUINELY AMBIGUOUS PARAMETER in this provider's
// documentation: it says "Base64 (without padding)" while linking the PKCE
// spec, which mandates base64URL. The two differ whenever the SHA-256 digest
// contains a byte mapping to `+` or `/` — which is most of the time — so the
// wrong choice fails INTERMITTENTLY, and an intermittent authorization failure
// is the worst kind to debug against a ten-minute code.
//
// base64url is used, because it is what the specification Zalo links mandates
// and what every other PKCE implementation sends. The standard-encoding
// alternative is named in the refusal an exchange produces rather than tried
// silently: an authorization that failed for this reason has a symptom nobody
// would otherwise connect to an encoding.
func challengeFor(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// permissionLink is the URL the OA admin opens.
func permissionLink(appID, redirectURI, challenge, state string) (string, error) {
	authorize, err := url.Parse(permissionURL)
	if err != nil {
		return "", fmt.Errorf("zalo-oa: parsing the permission endpoint: %w", err)
	}
	authorize.RawQuery = url.Values{
		"app_id":         {appID},
		"redirect_uri":   {redirectURI},
		"code_challenge": {challenge},
		"state":          {state},
	}.Encode()
	return authorize.String(), nil
}

// tokenPair is a grant as this unit holds it: the two credentials and when the
// access half stops being accepted.
//
// It is ONE value on purpose, and credential.go seals it as ONE document. The
// refresh token is single-use, so a rotation that wrote the two halves
// separately could leave a live access token beside a dead refresh token — a
// connection that works for a day and then cannot be renewed by anybody.
type tokenPair struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// usable reports whether the access token is worth spending at `now`.
//
// The margin is what makes the refresh PROACTIVE rather than a reaction to a
// 401 inside a send: a token that expires in the next hour is treated as spent,
// because discovering the expiry mid-transmission is how a refresh ends up
// racing a message.
func (p tokenPair) usable(now time.Time) bool {
	return p.AccessToken != "" && p.ExpiresAt.After(now.Add(refreshMargin))
}

// refreshMargin is how far ahead of the 25-hour expiry a token is renewed.
const refreshMargin = time.Hour

// grantExchanger is the token endpoint, injected the same way clientFactory is
// and for the same reason: it is a true boundary, and the serialization and
// persistence rules around it are this unit's own logic that has to be provable
// without a live grant.
type grantExchanger interface {
	// Redeem exchanges an authorization code for a first token pair.
	Redeem(ctx context.Context, appID, appSecret, code, verifier string) (tokenPair, error)
	// Rotate spends a refresh token and returns the pair that replaces it. The
	// token passed in is DEAD once this returns without an error.
	Rotate(ctx context.Context, appID, appSecret, refreshToken string) (tokenPair, error)
}

// oauthClient talks to the token endpoint.
type oauthClient struct {
	http *http.Client
	base string
}

// newOAuthClient builds the exchanger every production path uses.
func newOAuthClient() *oauthClient {
	return &oauthClient{
		http: &http.Client{
			Timeout: requestTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		base: tokenURL,
	}
}

// Redeem exchanges the code the admin's browser came back with.
//
// A refusal here IS the credential's: an authorization code is single-use and
// lives ten minutes, so a grant that did not come back is one no retry will
// produce. It is reported as such, with the two things that actually go wrong.
func (o *oauthClient) Redeem(ctx context.Context, appID, appSecret, code, verifier string) (tokenPair, error) {
	pair, err := o.grant(ctx, appSecret, url.Values{
		"code":          {code},
		"app_id":        {appID},
		"grant_type":    {"authorization_code"},
		"code_verifier": {verifier},
	})
	if errors.Is(err, errNoGrant) {
		return tokenPair{}, fmt.Errorf("%w: the authorization did not produce a token pair — the code may have been used already or expired (it is single-use and lives ten minutes), or the code challenge saved in the developer console was not the one that matches this verifier", errUnauthorized)
	}
	return pair, err
}

// Rotate spends the refresh token and returns its replacement.
//
// CALLING THIS IS DESTRUCTIVE. The token passed in is dead the moment the
// provider answers, whether or not this side manages to keep what came back —
// which is why the only caller holds a row lock and persists before it uses
// anything (credential.go).
func (o *oauthClient) Rotate(ctx context.Context, appID, appSecret, refreshToken string) (tokenPair, error) {
	pair, err := o.grant(ctx, appSecret, url.Values{
		"refresh_token": {refreshToken},
		"app_id":        {appID},
		"grant_type":    {"refresh_token"},
	})
	if errors.Is(err, errNoGrant) {
		// AN ANSWERED REFUSAL IS NOT PROOF THE CREDENTIAL IS DEAD, and the
		// asymmetry decides which way to read it. This endpoint's refusal codes are
		// not in the measured catalog — GUIDE.md §3 covers the OpenAPI host only —
		// so a rate limit, a disabled app or a maintenance document all arrive here
		// looking the same as a spent refresh token. Reading them as the credential
		// would park a working connection, and the only way back from that is an OA
		// admin at another company; reading them as the provider costs a retry on
		// the next tick.
		//
		// A refresh token that really is dead still parks, on measured evidence
		// rather than guessed: the access token expires behind it and the API then
		// answers -216, which IS in the catalog and IS the credential's refusal.
		return tokenPair{}, fmt.Errorf("%w: the token endpoint would not renew this credential, and did not say why in a way this unit reads", errProvider)
	}
	return pair, err
}

// grant posts one form to the token endpoint and reads what comes back.
func (o *oauthClient) grant(ctx context.Context, appSecret string, form url.Values) (tokenPair, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.base, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenPair{}, fmt.Errorf("%w: building the token request: %s", errProvider, err.Error())
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// The app secret rides its own header here, exactly as the access token does
	// on the OpenAPI host. It is never a form field and never a query argument.
	req.Header.Set("secret_key", appSecret)

	resp, err := o.http.Do(req)
	if err != nil {
		return tokenPair{}, fmt.Errorf("%w: %w: %s", errTransient, errUnanswered, err.Error())
	}
	//craft:ignore swallowed-errors best-effort close: the capped read below may leave the body mid-stream, so a close error carries no signal for this call's result
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return tokenPair{}, fmt.Errorf("%w: %w: reading the token answer: %s", errTransient, errUnanswered, err.Error())
	}
	if len(body) > maxResponseBytes {
		return tokenPair{}, fmt.Errorf("%w: the token answer is over the %d-byte cap", errProvider, maxResponseBytes)
	}
	return decodeGrant(body, time.Now())
}

// errNoGrant is an answer that carried no token pair. It is deliberately NOT a
// class of its own to a caller: what an unproductive answer MEANS differs by
// which grant asked for it, so each entry point maps this to the class its own
// failure has (see Redeem and Rotate).
const errNoGrant zaloError = "zalo-oa: the token endpoint returned no grant"

// decodeGrant reads the token endpoint's answer.
//
// It is a FLAT document rather than the OpenAPI envelope, and its `error` is
// sometimes a number and sometimes a string — so it is decoded as neither and
// the presence of the tokens is what decides. A grant with both halves is a
// grant; anything else is errNoGrant, and the provider's own text is deliberately
// not carried into it, because this string reaches an operator's screen.
func decodeGrant(body []byte, now time.Time) (tokenPair, error) {
	var answer struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		// expires_in is a STRING of seconds, not a number. Decoding it as an int
		// fails the whole document and reports a healthy grant as unreadable.
		ExpiresIn string `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		return tokenPair{}, fmt.Errorf("%w: the token endpoint answered something this unit cannot read", errProvider)
	}
	if answer.AccessToken == "" || answer.RefreshToken == "" {
		// Both halves or nothing. A grant missing the refresh token would connect
		// and then become unrenewable 25 hours later, with no signal in between.
		return tokenPair{}, errNoGrant
	}
	lifetime := time.Duration(atoiOrZero(answer.ExpiresIn)) * time.Second
	if lifetime <= 0 {
		// The documented lifetime, used when the provider does not state one.
		// Under-estimating is the safe direction: it renews early, and renewing
		// early costs one call while renewing late costs a dead connection.
		lifetime = defaultAccessLifetime
	}
	return tokenPair{
		AccessToken:  answer.AccessToken,
		RefreshToken: answer.RefreshToken,
		ExpiresAt:    now.Add(lifetime),
	}, nil
}

// defaultAccessLifetime is the documented 25 hours, used when a grant states no
// lifetime of its own.
const defaultAccessLifetime = 25 * time.Hour
