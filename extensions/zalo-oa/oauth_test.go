// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalooa

// The token endpoint: the one call this unit makes that spends a single-use
// credential.

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// A grant is BOTH halves or nothing. One missing the refresh token would connect
// and become unrenewable twenty-five hours later, with no signal in between.
func TestAGrantMissingEitherHalfIsRefused(t *testing.T) {
	for name, body := range map[string]string{
		"no refresh token": `{"access_token":"a","expires_in":"90000"}`,
		"no access token":  `{"refresh_token":"r","expires_in":"90000"}`,
		"an error instead": `{"error":-14014,"message":"code has expired"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeGrant([]byte(body), at(0)); !errors.Is(err, errNoGrant) {
				t.Fatalf("error = %v, want the no-grant answer", err)
			}
		})
	}
}

// AN UNPRODUCTIVE ANSWER IS CLASSIFIED BY ITS CALLER, not by the endpoint.
//
// This endpoint's refusal codes are not in the measured catalog — GUIDE.md §3
// covers the OpenAPI host only — so a rate limit and a spent token arrive looking
// alike. A connect has a human at the screen who supplied the token seconds ago
// and reads it as the credential's; a scheduled renewal has nobody, and reading
// it as the credential would park a working connection.
func TestAnUnproductiveAnswerIsLeftForItsCallerToClassify(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write([]byte(`{"error":-32,"message":"rate limit"}`)); err != nil {
			t.Errorf("writing the answer: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	client := newOAuthClient()
	client.base = server.URL

	_, err := client.Rotate(t.Context(), "app-1", "secret", "refresh")
	if !errors.Is(err, errNoGrant) {
		t.Fatalf("a renewal answered %v, want the unproductive answer left for its caller to classify", err)
	}
	// And the SCHEDULED caller reads it as the provider's, never the
	// credential's: parking a working connection costs an OA administrator at
	// another company, where a retry costs one tick.
	if errors.Is(rotationRefusal(err), errUnauthorized) {
		t.Fatalf("a scheduled renewal read an unexplained refusal as the credential: %v", err)
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
