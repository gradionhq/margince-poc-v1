// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package oauthflow

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

var (
	errAuth   = errors.New("auth rejected")
	errUnre   = errors.New("unreachable")
	baseScope = []string{"Mail.Read", "offline_access"}
)

func testConfig(tokenURL string, scopeInForms bool) Config {
	return Config{
		Provider:          "test",
		ClientID:          "cid",
		ClientSecret:      "secret",
		Scopes:            baseScope,
		AuthURL:           "https://idp.example/authorize",
		TokenURL:          tokenURL,
		AuthParams:        map[string]string{"response_mode": "query"},
		ScopeInTokenForms: scopeInForms,
		AuthRejected:      errAuth,
		Unreachable:       errUnre,
	}
}

func TestAuthCodeURLCarriesCommonAndProviderParams(t *testing.T) {
	c := New(testConfig("https://idp.example/token", false))
	raw := c.AuthCodeURL("st8", "https://app.example/cb")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	for k, want := range map[string]string{
		"client_id":     "cid",
		"redirect_uri":  "https://app.example/cb",
		"response_type": "code",
		"scope":         "Mail.Read offline_access",
		"state":         "st8",
		"response_mode": "query", // the provider-specific param merged in
	} {
		if q.Get(k) != want {
			t.Errorf("param %q = %q, want %q", k, q.Get(k), want)
		}
	}
}

// tokenServer answers the token endpoint with a scripted status + body and
// records the last form it received.
func tokenServer(t *testing.T, status int, body string, gotForm *url.Values) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		*gotForm = r.PostForm
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestExchangeReturnsRefreshTokenAndSendsScopeWhenConfigured(t *testing.T) {
	var form url.Values
	srv := tokenServer(t, http.StatusOK, `{"refresh_token":"r3fr3sh"}`, &form)

	c := New(testConfig(srv.URL, true))
	rt, err := c.Exchange(context.Background(), "code123", "https://app.example/cb")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if rt != "r3fr3sh" {
		t.Fatalf("refresh token = %q", rt)
	}
	if form.Get("grant_type") != "authorization_code" || form.Get("code") != "code123" {
		t.Fatalf("exchange form = %v", form)
	}
	if form.Get("scope") != "Mail.Read offline_access" {
		t.Fatal("ScopeInTokenForms=true must send the scope in the exchange form")
	}
}

func TestExchangeOmitsScopeWhenNotConfigured(t *testing.T) {
	var form url.Values
	srv := tokenServer(t, http.StatusOK, `{"refresh_token":"r"}`, &form)
	c := New(testConfig(srv.URL, false))
	if _, err := c.Exchange(context.Background(), "c", "cb"); err != nil {
		t.Fatal(err)
	}
	if form.Has("scope") {
		t.Fatal("ScopeInTokenForms=false must omit the scope from token forms")
	}
}

func TestExchangeNoRefreshTokenIsAuthRejected(t *testing.T) {
	var form url.Values
	srv := tokenServer(t, http.StatusOK, `{"access_token":"a"}`, &form) // 200 but no refresh_token
	c := New(testConfig(srv.URL, false))
	_, err := c.Exchange(context.Background(), "c", "cb")
	if !errors.Is(err, errAuth) {
		t.Fatalf("consent without a refresh token must be AuthRejected, got %v", err)
	}
}

func TestTokenStatusClassification(t *testing.T) {
	cases := []struct {
		status int
		want   error
	}{
		{http.StatusBadRequest, errAuth},   // 4xx → authorization problem
		{http.StatusUnauthorized, errAuth}, // 4xx
		{http.StatusInternalServerError, errUnre},
		{http.StatusBadGateway, errUnre},
	}
	for _, tc := range cases {
		var form url.Values
		srv := tokenServer(t, tc.status, "", &form)
		c := New(testConfig(srv.URL, false))
		_, err := c.AccessToken(context.Background(), "stored-refresh")
		if !errors.Is(err, tc.want) {
			t.Errorf("status %d → %v, want %v", tc.status, err, tc.want)
		}
	}
}

func TestAccessTokenSuccessAndMissingToken(t *testing.T) {
	var form url.Values
	srv := tokenServer(t, http.StatusOK, `{"access_token":"a-token"}`, &form)
	c := New(testConfig(srv.URL, false))
	at, err := c.AccessToken(context.Background(), "stored")
	if err != nil || at != "a-token" {
		t.Fatalf("AccessToken = %q, %v", at, err)
	}
	if form.Get("grant_type") != "refresh_token" || form.Get("refresh_token") != "stored" {
		t.Fatalf("refresh form = %v", form)
	}

	// A 200 that carries no access token is a rejected authorization, not a
	// silent empty success.
	srv2 := tokenServer(t, http.StatusOK, `{}`, &form)
	c2 := New(testConfig(srv2.URL, false))
	if _, err := c2.AccessToken(context.Background(), "stored"); !errors.Is(err, errAuth) {
		t.Fatalf("empty access token must be AuthRejected, got %v", err)
	}
}

func TestUndecodableBodyIsUnreachable(t *testing.T) {
	var form url.Values
	srv := tokenServer(t, http.StatusOK, `not json`, &form)
	c := New(testConfig(srv.URL, false))
	if _, err := c.AccessToken(context.Background(), "s"); !errors.Is(err, errUnre) {
		t.Fatalf("garbage token body must be Unreachable, got %v", err)
	}
}

func TestDialFailureIsUnreachable(t *testing.T) {
	// A token URL that cannot be reached (server closed) classifies as
	// unreachable, never a leaked transport error.
	c := New(testConfig("http://127.0.0.1:1/token", false))
	_, err := c.Exchange(context.Background(), "c", "cb")
	if !errors.Is(err, errUnre) {
		t.Fatalf("dial failure = %v, want Unreachable", err)
	}
	if strings.Contains(err.Error(), "connection refused") {
		t.Fatal("the raw transport error must not reach the caller")
	}
}

func TestTokenEndpointThrottleRateLimits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "45")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)

	c := New(testConfig(srv.URL, false))
	_, err := c.AccessToken(context.Background(), "stored")
	var rl *connector.RateLimitedError
	if !errors.As(err, &rl) {
		t.Fatalf("a 429 token response must rate-limit, got %v", err)
	}
	if rl.RetryAfter != 45*time.Second {
		t.Fatalf("Retry-After = %v, want 45s", rl.RetryAfter)
	}
}
