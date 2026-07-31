// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package oidc

// The relying party against a fake issuer. Every case here is a refusal the
// flow owes a human: an ID token arrives through the browser, so each check
// is the difference between authenticating someone and believing whoever
// asked. The clock is injected, so the expiry boundaries are asserted
// exactly rather than waited for.

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

const (
	testClientID = "margince.apps.googleusercontent.test"
	testKeyID    = "test-key-1"
	testNonce    = "the-nonce-from-the-login-state"
)

// fixedNow is the instant every case's clock reads, so `exp`/`iat` are
// arithmetic rather than timing.
var fixedNow = time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)

// fakeIssuer is an OIDC provider: discovery, a JWK Set, and a token
// endpoint. It records what the relying party sent, because half of what
// this flow must get right is the request (PKCE verifier, redirect URI)
// rather than the response.
type fakeIssuer struct {
	server *httptest.Server
	keys   map[string]*rsa.PrivateKey
	// activeKID is the key the token endpoint signs with; changing it
	// mid-test is a key rotation.
	activeKID string
	// publishedKIDs are the keys the JWK Set serves — a rotation publishes
	// the new key only after the token endpoint has already signed with it.
	publishedKIDs []string

	tokenForm url.Values
	// tokenStatus overrides the token endpoint's 200 for the failure cases.
	tokenStatus int
	// idTokenClaims overrides the default claim set; nil takes the valid one.
	claimOverride map[string]any
	// signAlgHeader overrides the JWS `alg` header, for algorithm confusion.
	signAlgHeader string
	// jwksRequests counts key-set fetches, which is how the cache and the
	// one forced refresh are observed.
	jwksRequests int
}

func newFakeIssuer(t *testing.T) *fakeIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating signing key: %v", err)
	}
	issuer := &fakeIssuer{
		keys:          map[string]*rsa.PrivateKey{testKeyID: key},
		activeKID:     testKeyID,
		publishedKIDs: []string{testKeyID},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", issuer.serveDiscovery)
	mux.HandleFunc("/jwks", issuer.serveJWKS)
	mux.HandleFunc("/token", issuer.serveToken)
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	// A token endpoint that answers 200 with an access token and NO id_token —
	// the shape a dropped `openid` scope produces. Registered here rather than
	// reached into httptest's internals from the case that uses it.
	mux.HandleFunc("/token-no-id", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"access_token": "not-an-identity"})
	})
	issuer.server = httptest.NewServer(mux)
	t.Cleanup(issuer.server.Close)
	return issuer
}

func (f *fakeIssuer) serveDiscovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"issuer":                 f.server.URL,
		"authorization_endpoint": f.server.URL + "/authorize",
		"token_endpoint":         f.server.URL + "/token",
		"jwks_uri":               f.server.URL + "/jwks",
	})
}

func (f *fakeIssuer) serveJWKS(w http.ResponseWriter, _ *http.Request) {
	f.jwksRequests++
	keys := make([]map[string]any, 0, len(f.publishedKIDs))
	for _, kid := range f.publishedKIDs {
		pub := f.keys[kid].PublicKey
		keys = append(keys, map[string]any{
			"kty": "RSA", "use": "sig", "alg": algRS256, "kid": kid,
			"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(bigEndian(pub.E)),
		})
	}
	writeJSON(w, map[string]any{"keys": keys})
}

func (f *fakeIssuer) serveToken(w http.ResponseWriter, r *http.Request) {
	body, err := readAll(r)
	if err != nil {
		f.refuse(w)
		return
	}
	form, err := url.ParseQuery(body)
	if err != nil {
		f.refuse(w)
		return
	}
	f.tokenForm = form
	if f.tokenStatus != 0 {
		w.WriteHeader(f.tokenStatus)
		writeJSON(w, map[string]any{"error": "invalid_grant"})
		return
	}
	writeJSON(w, map[string]any{"id_token": f.signIDToken(f.claims())})
}

// refuse answers a request the fixture could not even read as a form. It is
// an RFC 6749 refusal rather than a plain error page, so a broken fixture
// surfaces as a failed exchange instead of an unreadable transport error.
func (f *fakeIssuer) refuse(w http.ResponseWriter) {
	w.WriteHeader(http.StatusBadRequest)
	writeJSON(w, map[string]any{"error": "invalid_request"})
}

// claims is the valid claim set, before any case's override.
func (f *fakeIssuer) claims() map[string]any {
	claims := map[string]any{
		"iss": f.server.URL, "sub": "provider-subject-1", "aud": testClientID,
		"exp": fixedNow.Add(time.Hour).Unix(), "iat": fixedNow.Add(-time.Minute).Unix(),
		"nonce": testNonce, "email": "Ada@Example.Test", "email_verified": true,
		"name": "Ada Lovelace",
	}
	for k, v := range f.claimOverride {
		if v == nil {
			delete(claims, k)
			continue
		}
		claims[k] = v
	}
	return claims
}

// signIDToken mints a compact JWS over the claim set with the active key.
func (f *fakeIssuer) signIDToken(claims map[string]any) string {
	alg := algRS256
	if f.signAlgHeader != "" {
		alg = f.signAlgHeader
	}
	header := encodeSegment(map[string]any{"alg": alg, "typ": "JWT", "kid": f.activeKID})
	payload := encodeSegment(claims)
	signingInput := header + "." + payload
	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, f.keys[f.activeKID], crypto.SHA256, digest[:])
	if err != nil {
		// A fixture that cannot sign would silently turn every case into a
		// signature failure, which is the one outcome this must not fake.
		panic(fmt.Sprintf("signing the fixture id token: %v", err))
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
}

// rotate mints a second key, signs with it, and (optionally) leaves it
// unpublished so the forced-refresh path can be observed.
func (f *fakeIssuer) rotate(t *testing.T, publish bool) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating rotated key: %v", err)
	}
	const kid = "test-key-2"
	f.keys[kid] = key
	f.activeKID = kid
	if publish {
		f.publishedKIDs = append(f.publishedKIDs, kid)
	}
}

func (f *fakeIssuer) provider(t *testing.T) *Provider {
	t.Helper()
	p, err := New(Config{
		Issuer:       f.server.URL,
		ClientID:     testClientID,
		ClientSecret: "the-client-secret",
		RedirectURI:  "http://localhost:8080/v1/auth/oidc/google/callback",
		HTTPClient:   f.server.Client(),
		Now:          func() time.Time { return fixedNow },
	})
	if err != nil {
		t.Fatalf("building the relying party: %v", err)
	}
	return p
}

func TestExchangeAndVerifyAcceptsAValidToken(t *testing.T) {
	issuer := newFakeIssuer(t)
	p := issuer.provider(t)

	identity, err := p.ExchangeAndVerify(context.Background(), "the-code", "the-verifier", testNonce)
	if err != nil {
		t.Fatalf("ExchangeAndVerify: %v", err)
	}
	if identity.Subject != "provider-subject-1" {
		t.Errorf("subject = %q, want provider-subject-1", identity.Subject)
	}
	// The email is lower-cased here so every downstream match is against one
	// spelling — the provider is free to echo the address as the human typed it.
	if identity.Email != "ada@example.test" {
		t.Errorf("email = %q, want the lower-cased address", identity.Email)
	}
	if identity.Issuer != issuer.server.URL {
		t.Errorf("issuer = %q, want %q", identity.Issuer, issuer.server.URL)
	}

	// The exchange is what binds the code to this browser and this client:
	// without the verifier a stolen code is redeemable by anyone.
	if got := issuer.tokenForm.Get("code_verifier"); got != "the-verifier" {
		t.Errorf("code_verifier = %q, want the stored verifier", got)
	}
	if got := issuer.tokenForm.Get("client_secret"); got != "the-client-secret" {
		t.Errorf("client_secret = %q, want the configured secret", got)
	}
	if got := issuer.tokenForm.Get("redirect_uri"); got != "http://localhost:8080/v1/auth/oidc/google/callback" {
		t.Errorf("redirect_uri = %q, want the registered value", got)
	}
	if got := issuer.tokenForm.Get("grant_type"); got != "authorization_code" {
		t.Errorf("grant_type = %q, want authorization_code", got)
	}
}

func TestAuthCodeURLCarriesTheS256ChallengeAndNonce(t *testing.T) {
	issuer := newFakeIssuer(t)
	p := issuer.provider(t)
	req := AuthRequest{State: "the-state", Nonce: "the-nonce", Verifier: "the-verifier"}

	raw, err := p.AuthCodeURL(context.Background(), req, "example.test")
	if err != nil {
		t.Fatalf("AuthCodeURL: %v", err)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parsing the authorization url: %v", err)
	}
	q := parsed.Query()
	// S256, never `plain`: the challenge must not be the verifier itself, or
	// anything that intercepts the redirect can replay it.
	if got := q.Get("code_challenge_method"); got != "S256" {
		t.Errorf("code_challenge_method = %q, want S256", got)
	}
	sum := sha256.Sum256([]byte("the-verifier"))
	if got, want := q.Get("code_challenge"), base64.RawURLEncoding.EncodeToString(sum[:]); got != want {
		t.Errorf("code_challenge = %q, want %q", got, want)
	}
	if q.Get("code_challenge") == "the-verifier" {
		t.Error("the challenge is the verifier in the clear")
	}
	for key, want := range map[string]string{
		"state": "the-state", "nonce": "the-nonce", "response_type": "code",
		"client_id": testClientID, "hd": "example.test",
	} {
		if got := q.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if !strings.Contains(q.Get("scope"), "openid") {
		t.Errorf("scope = %q, want it to request openid", q.Get("scope"))
	}
}

func TestNewAuthRequestMintsThreeDistinctHighEntropySecrets(t *testing.T) {
	first, err := NewAuthRequest()
	if err != nil {
		t.Fatalf("NewAuthRequest: %v", err)
	}
	second, err := NewAuthRequest()
	if err != nil {
		t.Fatalf("NewAuthRequest: %v", err)
	}
	// The three must differ from each other AND across attempts: a state that
	// doubles as the nonce proves nothing the state did not already prove.
	for _, pair := range [][2]string{
		{first.State, first.Nonce},
		{first.State, first.Verifier},
		{first.Nonce, first.Verifier},
		{first.State, second.State},
		{first.Nonce, second.Nonce},
		{first.Verifier, second.Verifier},
	} {
		if pair[0] == pair[1] {
			t.Errorf("two secrets are equal (%q)", pair[0])
		}
	}
	// 32 random bytes, base64url — 43 characters, comfortably over the
	// 128-bit floor and inside RFC 7636's verifier length range.
	if len(first.Verifier) != 43 {
		t.Errorf("verifier length = %d, want 43", len(first.Verifier))
	}
}

// The table of refusals: each row is a claim set or a header the provider
// could return and this relying party must not accept.
func TestVerifyIDTokenRefusesEveryUnprovenToken(t *testing.T) {
	cases := []struct {
		name      string
		claims    map[string]any
		algHeader string
		wantErr   error
	}{
		{
			name:    "another issuer",
			claims:  map[string]any{"iss": "https://accounts.evil.test"},
			wantErr: ErrTokenInvalid,
		},
		{
			name:    "another client's audience",
			claims:  map[string]any{"aud": "someone-else.apps.googleusercontent.test"},
			wantErr: ErrTokenInvalid,
		},
		{
			name:    "multi-audience token authorized to another party",
			claims:  map[string]any{"aud": []string{testClientID, "other"}, "azp": "other"},
			wantErr: ErrTokenInvalid,
		},
		{
			name:    "expired",
			claims:  map[string]any{"exp": fixedNow.Add(-2 * time.Minute).Unix()},
			wantErr: ErrTokenInvalid,
		},
		{
			name:    "issued in the future",
			claims:  map[string]any{"iat": fixedNow.Add(10 * time.Minute).Unix()},
			wantErr: ErrTokenInvalid,
		},
		{
			name:    "issued too long ago to be a fresh authentication",
			claims:  map[string]any{"iat": fixedNow.Add(-time.Hour).Unix()},
			wantErr: ErrTokenInvalid,
		},
		{
			name:    "no subject",
			claims:  map[string]any{"sub": nil},
			wantErr: ErrTokenInvalid,
		},
		{
			name:    "nonce from another attempt",
			claims:  map[string]any{"nonce": "somebody-else's-nonce"},
			wantErr: ErrTokenInvalid,
		},
		{
			name:    "no nonce at all",
			claims:  map[string]any{"nonce": nil},
			wantErr: ErrTokenInvalid,
		},
		{
			name:    "email the provider has not verified",
			claims:  map[string]any{"email_verified": false},
			wantErr: ErrEmailUnverified,
		},
		{
			name:    "no email_verified claim",
			claims:  map[string]any{"email_verified": nil},
			wantErr: ErrEmailUnverified,
		},
		{
			name:    "no email",
			claims:  map[string]any{"email": nil},
			wantErr: ErrTokenInvalid,
		},
		{
			name:      "unsigned token (alg none)",
			algHeader: "none",
			wantErr:   ErrTokenInvalid,
		},
		{
			name:      "symmetric alg, so the public key would be the secret",
			algHeader: "HS256",
			wantErr:   ErrTokenInvalid,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			issuer := newFakeIssuer(t)
			issuer.claimOverride, issuer.signAlgHeader = tc.claims, tc.algHeader
			p := issuer.provider(t)

			_, err := p.ExchangeAndVerify(context.Background(), "the-code", "the-verifier", testNonce)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestVerifyIDTokenRefusesAForgedSignature(t *testing.T) {
	issuer := newFakeIssuer(t)
	p := issuer.provider(t)
	// A token signed by a key the provider never published: the attacker owns
	// a keypair and claims the provider's issuer.
	forged := newFakeIssuer(t)
	forged.claimOverride = map[string]any{"iss": issuer.server.URL}

	_, err := p.VerifyIDToken(context.Background(), forged.signIDToken(forged.claims()), testNonce)
	if !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("err = %v, want ErrTokenInvalid", err)
	}
}

func TestVerifyIDTokenRefusesATamperedClaimSet(t *testing.T) {
	issuer := newFakeIssuer(t)
	p := issuer.provider(t)
	token := issuer.signIDToken(issuer.claims())

	// Swap the payload for one naming a different subject, keeping the real
	// signature: the classic "the claims are just base64" attempt.
	parts := strings.Split(token, ".")
	parts[1] = encodeSegment(map[string]any{
		"iss": issuer.server.URL, "sub": "somebody-elses-subject", "aud": testClientID,
		"exp": fixedNow.Add(time.Hour).Unix(), "iat": fixedNow.Unix(),
		"nonce": testNonce, "email": "root@example.test", "email_verified": true,
	})

	_, err := p.VerifyIDToken(context.Background(), strings.Join(parts, "."), testNonce)
	if !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("err = %v, want ErrTokenInvalid", err)
	}
}

func TestVerifyIDTokenRefreshesTheKeySetOnceForARotatedKey(t *testing.T) {
	issuer := newFakeIssuer(t)
	p := issuer.provider(t)

	// Warm the cache with the pre-rotation key.
	if _, err := p.ExchangeAndVerify(context.Background(), "code-1", "verifier-1", testNonce); err != nil {
		t.Fatalf("first sign-in: %v", err)
	}
	if issuer.jwksRequests != 1 {
		t.Fatalf("jwks requests after the first sign-in = %d, want 1", issuer.jwksRequests)
	}

	// The provider rotates and signs with a key the cached set predates. That
	// is ordinary provider behaviour, so it must resolve — with exactly one
	// extra fetch, never a fetch per token.
	issuer.rotate(t, true)
	if _, err := p.ExchangeAndVerify(context.Background(), "code-2", "verifier-2", testNonce); err != nil {
		t.Fatalf("sign-in after key rotation: %v", err)
	}
	if issuer.jwksRequests != 2 {
		t.Errorf("jwks requests after rotation = %d, want 2 (one forced refresh)", issuer.jwksRequests)
	}
}

func TestVerifyIDTokenRefusesAKeyTheProviderNeverPublished(t *testing.T) {
	issuer := newFakeIssuer(t)
	p := issuer.provider(t)
	// Signed with a key that stays out of the JWK Set: the forced refresh
	// finds nothing, and the refusal is final rather than a retry loop.
	issuer.rotate(t, false)

	_, err := p.ExchangeAndVerify(context.Background(), "the-code", "the-verifier", testNonce)
	if !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("err = %v, want ErrTokenInvalid", err)
	}
	if issuer.jwksRequests == 0 {
		t.Error("the key set was never fetched")
	}
}

func TestExchangeClassifiesProviderFailures(t *testing.T) {
	t.Run("a 4xx is the authorization being refused", func(t *testing.T) {
		issuer := newFakeIssuer(t)
		issuer.tokenStatus = http.StatusBadRequest
		p := issuer.provider(t)

		_, err := p.ExchangeAndVerify(context.Background(), "stale-code", "the-verifier", testNonce)
		if !errors.Is(err, ErrAuthorizationRejected) {
			t.Fatalf("err = %v, want ErrAuthorizationRejected", err)
		}
		// The RFC 6749 code is a closed vocabulary and safe in an operator
		// log; the provider's prose is not, and must not be here.
		if !strings.Contains(err.Error(), "invalid_grant") {
			t.Errorf("err = %v, want it to name the oauth error code", err)
		}
	})

	t.Run("a 5xx is the provider being unavailable", func(t *testing.T) {
		issuer := newFakeIssuer(t)
		issuer.tokenStatus = http.StatusBadGateway
		p := issuer.provider(t)

		_, err := p.ExchangeAndVerify(context.Background(), "the-code", "the-verifier", testNonce)
		if !errors.Is(err, ErrProviderUnavailable) {
			t.Fatalf("err = %v, want ErrProviderUnavailable", err)
		}
	})

	t.Run("a 200 with no id_token is not an authentication", func(t *testing.T) {
		issuer := newFakeIssuer(t)
		p := issuer.provider(t)
		// Point the cached discovery document at the id-token-less endpoint.
		doc, err := p.discover(context.Background())
		if err != nil {
			t.Fatalf("discovery: %v", err)
		}
		doc.TokenEndpoint = issuer.server.URL + "/token-no-id"
		p.doc = &doc

		_, err = p.ExchangeAndVerify(context.Background(), "the-code", "the-verifier", testNonce)
		if !errors.Is(err, ErrAuthorizationRejected) {
			t.Fatalf("err = %v, want ErrAuthorizationRejected", err)
		}
	})
}

func TestDiscoveryRefusesADocumentForAnotherIssuer(t *testing.T) {
	// A discovery URL that answers with SOMEBODY ELSE's issuer: without the
	// OIDC Discovery §4.3 equality check, a redirect at the well-known path
	// could substitute a whole provider.
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"issuer":                 "https://accounts.evil.test",
			"authorization_endpoint": "https://accounts.evil.test/authorize",
			"token_endpoint":         "https://accounts.evil.test/token",
			"jwks_uri":               "https://accounts.evil.test/jwks",
		})
	}))
	t.Cleanup(other.Close)

	p, err := New(Config{
		Issuer: other.URL, ClientID: testClientID, ClientSecret: "s",
		RedirectURI: "http://localhost:8080/cb", HTTPClient: other.Client(),
		Now: func() time.Time { return fixedNow },
	})
	if err != nil {
		t.Fatalf("building the relying party: %v", err)
	}
	if _, err := p.AuthCodeURL(context.Background(), AuthRequest{State: "s", Nonce: "n", Verifier: "v"}, ""); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("err = %v, want ErrProviderUnavailable", err)
	}
}

func TestNewRefusesAPlainHTTPIssuerThatIsNotLoopback(t *testing.T) {
	_, err := New(Config{
		Issuer: "http://accounts.example.test", ClientID: testClientID,
		ClientSecret: "s", RedirectURI: "http://localhost:8080/cb",
	})
	if err == nil {
		t.Fatal("a plain-http remote issuer was accepted; an id token fetched over http proves nothing")
	}
}

func TestNewRefusesAnIncompleteRelyingParty(t *testing.T) {
	for _, missing := range []string{"issuer", "client id", "client secret", "redirect uri"} {
		cfg := Config{
			Issuer: "https://accounts.google.com", ClientID: testClientID,
			ClientSecret: "s", RedirectURI: "http://localhost:8080/cb",
		}
		switch missing {
		case "issuer":
			cfg.Issuer = ""
		case "client id":
			cfg.ClientID = ""
		case "client secret":
			cfg.ClientSecret = ""
		case "redirect uri":
			cfg.RedirectURI = ""
		}
		if _, err := New(cfg); err == nil {
			t.Errorf("a relying party with no %s was accepted", missing)
		}
	}
}

func TestCacheTTLFollowsTheProvidersHeaderWithinBounds(t *testing.T) {
	cases := []struct {
		header string
		want   time.Duration
	}{
		{"", defaultCacheTTL},
		{"public, max-age=7200", 2 * time.Hour},
		// Floored: a provider that says "never cache" must not turn every
		// sign-in into three round-trips.
		{"no-store", defaultCacheTTL},
		{"max-age=1", minCacheTTL},
		// Capped, and a malformed value is no signal at all.
		{"max-age=999999999", 24 * time.Hour},
		{"max-age=soon", defaultCacheTTL},
	}
	for _, tc := range cases {
		if got := cacheTTL(tc.header); got != tc.want {
			t.Errorf("cacheTTL(%q) = %v, want %v", tc.header, got, tc.want)
		}
	}
}

func TestRSAKeysSkipsUnusableKeysAndRefusesAnEmptySet(t *testing.T) {
	small, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("generating the undersized key: %v", err)
	}
	var parsed jwksResponse
	if err := json.Unmarshal([]byte(`{"keys":[
		{"kty":"EC","kid":"ec-1","crv":"P-256"},
		{"kty":"RSA","kid":"enc-1","use":"enc","n":"AQAB","e":"AQAB"},
		{"kty":"RSA","kid":"ps-1","alg":"PS256","n":"AQAB","e":"AQAB"}
	]}`), &parsed); err != nil {
		t.Fatalf("decoding the fixture key set: %v", err)
	}
	// Nothing verifiable with RS256 is left, so this is a provider problem —
	// every sign-in through it would fail the same way.
	if _, err := rsaKeys(parsed); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("err = %v, want ErrProviderUnavailable", err)
	}

	// An undersized modulus is skipped rather than trusted: a short key
	// verifies happily and proves nothing.
	undersized := jwksResponse{Keys: []jwk{{
		Kty: "RSA", Kid: "small-1", Use: "sig", Alg: algRS256,
		N: base64.RawURLEncoding.EncodeToString(small.N.Bytes()),
		E: base64.RawURLEncoding.EncodeToString(bigEndian(small.E)),
	}}}
	if _, err := rsaKeys(undersized); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("undersized key err = %v, want ErrProviderUnavailable", err)
	}
}

func TestSplitJWSRefusesMalformedTokens(t *testing.T) {
	for _, raw := range []string{
		"", "one-part", "two.parts", "a.b.c.d",
		"!!!." + encodeSegment(map[string]any{}) + ".sig",
		encodeSegment(map[string]any{"alg": algRS256}) + ".!!!.sig",
	} {
		if _, _, _, err := splitJWS(raw); !errors.Is(err, ErrTokenInvalid) {
			t.Errorf("splitJWS(%q) err = %v, want ErrTokenInvalid", raw, err)
		}
	}
}

// --- fixture helpers ---

func encodeSegment(value map[string]any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("encoding a fixture jws segment: %v", err))
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func writeJSON(w http.ResponseWriter, value map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		panic(fmt.Sprintf("writing a fixture response: %v", err))
	}
}

func readAll(r *http.Request) (string, error) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// bigEndian renders an RSA public exponent as the JWK's base64url bytes.
func bigEndian(exponent int) []byte {
	out := []byte{byte(exponent >> 16), byte(exponent >> 8), byte(exponent)}
	for len(out) > 1 && out[0] == 0 {
		out = out[1:]
	}
	return out
}

func TestSafeReasonBoundsAnUntrustedErrorCode(t *testing.T) {
	// Every value here reaches us from outside — a provider's refusal body, or
	// the browser's own `?error=` on an unauthenticated callback — and lands in
	// an operator log line. Anything that is not a plain RFC 6749 code is
	// replaced rather than logged, so nobody chooses how many bytes of their
	// own text the log carries.
	for raw, want := range map[string]string{
		"access_denied":              "access_denied",
		"invalid_grant":              "invalid_grant",
		"":                           unspecifiedReason,
		"has spaces":                 unspecifiedReason,
		"newline\ninjected":          unspecifiedReason,
		"digits123":                  unspecifiedReason,
		"punctuation!":               unspecifiedReason,
		strings.Repeat("A", 64):      strings.Repeat("A", 64),
		strings.Repeat("A", 65):      unspecifiedReason,
		strings.Repeat("A", 900_000): unspecifiedReason,
	} {
		if got := SafeReason(raw); got != want {
			t.Errorf("SafeReason(%.20q…) = %q, want %q", raw, got, want)
		}
	}
}

func TestDiscoveryFailureIsNotRetriedOnEveryRequest(t *testing.T) {
	// The start endpoint is unauthenticated and discovers on every hit, under
	// the cache lock. Without a failure floor, a blackholed provider would give
	// every arriving request its own full timeout, queued behind the last one.
	attempts := 0
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(down.Close)

	now := fixedNow
	p, err := New(Config{
		Issuer: down.URL, ClientID: testClientID, ClientSecret: "s",
		RedirectURI: "http://localhost:8080/cb", HTTPClient: down.Client(),
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("building the relying party: %v", err)
	}
	req := AuthRequest{State: "s", Nonce: "n", Verifier: "v"}
	for range 5 {
		if _, err := p.AuthCodeURL(context.Background(), req, ""); !errors.Is(err, ErrProviderUnavailable) {
			t.Fatalf("err = %v, want ErrProviderUnavailable", err)
		}
	}
	if attempts != 1 {
		t.Errorf("provider was dialled %d times inside the backoff window, want 1", attempts)
	}

	// Past the window it is tried again — a provider that comes back is picked
	// up, so this is a floor and not a circuit breaker that stays open.
	now = fixedNow.Add(failureBackoff + time.Second)
	if _, err := p.AuthCodeURL(context.Background(), req, ""); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("err = %v, want ErrProviderUnavailable", err)
	}
	if attempts != 2 {
		t.Errorf("provider was dialled %d times in total, want 2 (one per window)", attempts)
	}
}
