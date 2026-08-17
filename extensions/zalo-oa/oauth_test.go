// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalooa

// The browser round trip and the token endpoint behind it.

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The verifier is drawn fresh each time, is the length Zalo specifies exactly,
// and uses only the alphabet Zalo admits — which is NARROWER than RFC 7636's,
// and it is the provider that validates it.
func TestAVerifierIsFreshAndInsideTheProvidersOwnAlphabet(t *testing.T) {
	first, err := newVerifier()
	if err != nil {
		t.Fatalf("newVerifier: %v", err)
	}
	second, err := newVerifier()
	if err != nil {
		t.Fatalf("newVerifier: %v", err)
	}
	if first == second {
		t.Fatal("two verifiers came out the same; a PKCE verifier that repeats is not one")
	}
	for _, verifier := range []string{first, second} {
		if len(verifier) != verifierLength {
			t.Fatalf("the verifier is %d characters, want exactly %d", len(verifier), verifierLength)
		}
		if strings.ContainsFunc(verifier, func(r rune) bool {
			return !strings.ContainsRune(verifierAlphabet, r)
		}) {
			t.Fatalf("the verifier %q uses characters outside the alphabet the provider admits", verifier)
		}
	}
}

// THE CHALLENGE IS BASE64URL. Zalo's documentation says "Base64 (without
// padding)" while linking the PKCE spec, which mandates base64URL; the two differ
// whenever the digest contains a byte mapping to `+` or `/`, which is most of the
// time — so the wrong choice fails intermittently, against a code that lives ten
// minutes. base64url is what the linked specification mandates and what every
// other implementation sends.
func TestTheCodeChallengeIsBase64URLAndCarriesNoPadding(t *testing.T) {
	// A verifier whose digest is known to contain bytes that differ between the
	// two encodings, found by construction rather than assumed.
	var verifier string
	for i := range 200 {
		candidate := strings.Repeat("a", verifierLength-3) + string(rune('a'+i%26)) + "bc"
		sum := sha256.Sum256([]byte(candidate))
		if base64.RawStdEncoding.EncodeToString(sum[:]) != base64.RawURLEncoding.EncodeToString(sum[:]) {
			verifier = candidate
			break
		}
	}
	if verifier == "" {
		t.Fatal("no verifier was found whose digest distinguishes the two encodings; the test proves nothing without one")
	}
	sum := sha256.Sum256([]byte(verifier))
	got := challengeFor(verifier)
	if got != base64.RawURLEncoding.EncodeToString(sum[:]) {
		t.Fatalf("challenge = %q, want the base64url form", got)
	}
	if got == base64.RawStdEncoding.EncodeToString(sum[:]) {
		t.Fatal("the challenge matches the standard encoding for a digest where the two differ")
	}
	if strings.Contains(got, "=") {
		t.Fatalf("challenge = %q, want no padding", got)
	}
}

// The permission URL carries what the provider needs and nothing this unit
// invented, and the state binds the redirect back to the authorization that
// started it.
func TestThePermissionURLCarriesTheAppTheChallengeAndTheState(t *testing.T) {
	link, err := permissionLink("app-1", "https://crm.example.com/zalo", "chal", "state-1")
	if err != nil {
		t.Fatalf("permissionLink: %v", err)
	}
	parsed, err := url.Parse(link)
	if err != nil {
		t.Fatalf("the permission URL is not a URL: %v", err)
	}
	for key, want := range map[string]string{
		"app_id": "app-1", "redirect_uri": "https://crm.example.com/zalo",
		"code_challenge": "chal", "state": "state-1",
	} {
		if got := parsed.Query().Get(key); got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
	if !strings.HasPrefix(link, permissionURL) {
		t.Fatalf("the permission URL points at %q rather than the provider's own endpoint", link)
	}
}

// A grant is BOTH halves or nothing. One missing the refresh token would connect
// and become unrenewable twenty-five hours later, with no signal in between.
func TestAGrantMissingEitherHalfIsRefused(t *testing.T) {
	for name, body := range map[string]string{
		"no refresh token": `{"access_token":"a","expires_in":"90000"}`,
		"no access token":  `{"refresh_token":"r","expires_in":"90000"}`,
		"an error instead": `{"error":-14014,"message":"code has expired"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeGrant([]byte(body), at(0)); !errors.Is(err, errUnauthorized) {
				t.Fatalf("error = %v, want the authorization refusal", err)
			}
		})
	}
}

// `expires_in` is a STRING of seconds, not a number. Decoding it as an int fails
// the whole document and reports a healthy grant as unreadable.
func TestTheGrantsLifetimeIsReadFromAQuotedNumber(t *testing.T) {
	pair, err := decodeGrant([]byte(`{"access_token":"a","refresh_token":"r","expires_in":"90000"}`), at(0))
	if err != nil {
		t.Fatalf("decodeGrant: %v", err)
	}
	if want := at(90000 * time.Second); !pair.ExpiresAt.Equal(want) {
		t.Fatalf("ExpiresAt = %s, want %s", pair.ExpiresAt, want)
	}
}

// A grant stating no lifetime gets the documented 25 hours. Under-estimating is
// the safe direction: renewing early costs one call, and renewing late costs the
// connection.
func TestAGrantWithNoStatedLifetimeTakesTheDocumentedOne(t *testing.T) {
	pair, err := decodeGrant([]byte(`{"access_token":"a","refresh_token":"r"}`), at(0))
	if err != nil {
		t.Fatalf("decodeGrant: %v", err)
	}
	if want := at(defaultAccessLifetime); !pair.ExpiresAt.Equal(want) {
		t.Fatalf("ExpiresAt = %s, want the documented %s", pair.ExpiresAt, want)
	}
}

// A token inside its last hour is treated as spent, which is what makes the
// renewal proactive rather than a reaction to a refusal inside a send.
func TestATokenInsideTheRenewalMarginIsNotUsable(t *testing.T) {
	pair := tokenPair{AccessToken: "a", RefreshToken: "r", ExpiresAt: at(refreshMargin / 2)}
	if pair.usable(at(0)) {
		t.Fatal("a token expiring inside the margin reported itself usable; the renewal would then race a message")
	}
	pair.ExpiresAt = at(refreshMargin * 2)
	if !pair.usable(at(0)) {
		t.Fatal("a token well inside its life reported itself unusable")
	}
	if (tokenPair{ExpiresAt: at(time.Hour * 24)}).usable(at(0)) {
		t.Fatal("a pair with no access token reported itself usable")
	}
}

// The app secret rides its own header, and the form carries the grant type the
// provider names. Both are checked against the wire because the token endpoint is
// the one call in this unit whose failure is unrecoverable.
func TestTheTokenEndpointIsCalledWithTheSecretInItsOwnHeader(t *testing.T) {
	var (
		secretHeader string
		form         url.Values
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secretHeader = r.Header.Get("secret_key")
		if err := r.ParseForm(); err != nil {
			t.Errorf("parsing the form: %v", err)
		}
		form = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{"access_token":"a","refresh_token":"r","expires_in":"90000"}`)); err != nil {
			t.Errorf("writing the answer: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	client := newOAuthClient()
	client.base = server.URL
	if _, err := client.Rotate(t.Context(), "app-1", "the-secret", "old-refresh"); err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if secretHeader != "the-secret" {
		t.Fatalf("secret_key header = %q, want the app secret", secretHeader)
	}
	if form.Get("grant_type") != "refresh_token" {
		t.Fatalf("grant_type = %q, want refresh_token", form.Get("grant_type"))
	}
	if form.Get("refresh_token") != "old-refresh" {
		t.Fatalf("refresh_token = %q, want the token being spent", form.Get("refresh_token"))
	}
	if form.Get("code_verifier") != "" {
		t.Fatal("a rotation carried a code verifier, which belongs only to a first redemption")
	}
}

// A redemption carries the verifier that minted the challenge; without it the
// provider cannot check the binding and the code is spent for nothing.
func TestARedemptionCarriesTheCodeAndItsVerifier(t *testing.T) {
	var form url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parsing the form: %v", err)
		}
		form = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{"access_token":"a","refresh_token":"r","expires_in":"90000"}`)); err != nil {
			t.Errorf("writing the answer: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	client := newOAuthClient()
	client.base = server.URL
	if _, err := client.Redeem(t.Context(), "app-1", "secret", "the-code", "the-verifier"); err != nil {
		t.Fatalf("Redeem: %v", err)
	}
	if form.Get("grant_type") != "authorization_code" {
		t.Fatalf("grant_type = %q, want authorization_code", form.Get("grant_type"))
	}
	if form.Get("code") != "the-code" || form.Get("code_verifier") != "the-verifier" {
		t.Fatalf("the redemption carried %v, want the code and its verifier", form)
	}
}

// A token endpoint that never answers carries the unanswered class, which is what
// tells the renewal that the outcome of a single-use rotation is unknown.
func TestATokenEndpointThatNeverAnswersIsUnanswered(t *testing.T) {
	client := newOAuthClient()
	client.base = "http://127.0.0.1:1"
	_, err := client.Rotate(t.Context(), "app-1", "secret", "old")
	if !errors.Is(err, errUnanswered) {
		t.Fatalf("error = %v, want the unanswered class — a rotation whose outcome is unknown must not be retried", err)
	}
}
