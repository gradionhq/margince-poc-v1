// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package googleconn

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// fakeOAuth is a stub Google OAuth2 handshake for the Authenticate tests.
type fakeOAuth struct {
	refresh, access string
	exchangeErr     error
	accessErr       error
}

func (fakeOAuth) AuthCodeURL(state, _ string) string { return "https://auth?state=" + state }
func (f fakeOAuth) Exchange(context.Context, string, string) (string, error) {
	return f.refresh, f.exchangeErr
}

func (f fakeOAuth) AccessToken(context.Context, string) (string, error) {
	return f.access, f.accessErr
}

func TestBoundedClientHasTimeout(t *testing.T) {
	if c := BoundedClient(); c.Timeout != httpTimeout {
		t.Errorf("BoundedClient timeout = %v, want %v", c.Timeout, httpTimeout)
	}
}

func TestScopeStringsRendersScopes(t *testing.T) {
	got := ScopeStrings([]principal.Scope{principal.ScopeRead})
	if len(got) != 1 || got[0] != string(principal.ScopeRead) {
		t.Errorf("ScopeStrings = %v, want [%q]", got, principal.ScopeRead)
	}
	if got := ScopeStrings(nil); len(got) != 0 {
		t.Errorf("ScopeStrings(nil) = %v, want empty", got)
	}
}

func TestAuthRequestFromRoundTrips(t *testing.T) {
	req, err := AuthRequestFrom("the-code", "https://app/callback")
	if err != nil {
		t.Fatalf("AuthRequestFrom: %v", err)
	}
	var p authPayload
	if err := json.Unmarshal(req.Payload, &p); err != nil {
		t.Fatalf("payload not decodable: %v", err)
	}
	if p.Code != "the-code" || p.RedirectURI != "https://app/callback" {
		t.Errorf("payload = %+v, want the-code / the callback", p)
	}
}

func TestAuthenticateSealsRefreshTokenAndOwner(t *testing.T) {
	req, err := AuthRequestFrom("the-code", "https://app/callback")
	if err != nil {
		t.Fatalf("AuthRequestFrom: %v", err)
	}
	owner := func(context.Context, string) (string, error) { return "rep@myco.com", nil }
	auth, err := Authenticate(context.Background(), fakeOAuth{refresh: "refresh-1", access: "access-1"}, req,
		[]principal.Scope{principal.ScopeRead}, owner)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	var st AuthState
	if err := json.Unmarshal(auth, &st); err != nil {
		t.Fatalf("auth is not AuthState json: %v", err)
	}
	if st.RefreshToken != "refresh-1" || st.Owner != "rep@myco.com" || len(st.Scopes) != 1 {
		t.Errorf("AuthState = %+v, want refresh-1 / rep@myco.com / [read]", st)
	}
}

func TestAuthenticateRejectsMissingCode(t *testing.T) {
	req, err := AuthRequestFrom("", "https://app/callback")
	if err != nil {
		t.Fatalf("AuthRequestFrom: %v", err)
	}
	owner := func(context.Context, string) (string, error) { return "x", nil }
	if _, err := Authenticate(context.Background(), fakeOAuth{}, req, nil, owner); !errors.Is(err, ErrAuthRejected) {
		t.Fatalf("want ErrAuthRejected for a missing code, got %v", err)
	}
}

func TestAuthenticatePropagatesOwnerError(t *testing.T) {
	req, err := AuthRequestFrom("the-code", "https://app/callback")
	if err != nil {
		t.Fatalf("AuthRequestFrom: %v", err)
	}
	boom := errors.New("owner lookup failed")
	owner := func(context.Context, string) (string, error) { return "", boom }
	if _, err := Authenticate(context.Background(), fakeOAuth{refresh: "r", access: "a"}, req, nil, owner); !errors.Is(err, boom) {
		t.Fatalf("want the owner-resolution error, got %v", err)
	}
}

func TestAuthenticateRejectsMalformedPayload(t *testing.T) {
	owner := func(context.Context, string) (string, error) { return "x", nil }
	if _, err := Authenticate(context.Background(), fakeOAuth{}, connector.AuthRequest{Payload: []byte("}bad{")}, nil, owner); err == nil {
		t.Fatal("Authenticate must reject a malformed auth payload")
	}
}

func TestGetDecodesOKAndMapsSentinels(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/ok", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		//craft:ignore swallowed-errors test stub encode
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "rep@myco.com"})
	})
	mux.HandleFunc("/forbidden", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) })
	mux.HandleFunc("/toomany", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	mux.HandleFunc("/quota", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		//craft:ignore swallowed-errors test stub write
		_, _ = w.Write([]byte(`{"error":{"errors":[{"reason":"rateLimitExceeded"}]}}`))
	})
	mux.HandleFunc("/boom", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) })
	mux.HandleFunc("/garbage", func(w http.ResponseWriter, _ *http.Request) {
		//craft:ignore swallowed-errors test stub write
		_, _ = w.Write([]byte("not json"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := srv.Client()

	var out struct {
		ID string `json:"id"`
	}
	if status, err := Get(context.Background(), client, srv.URL, "tok", "/ok", nil, &out); err != nil || status != http.StatusOK || out.ID != "rep@myco.com" {
		t.Fatalf("Get /ok = (%d, %v), id=%q; want (200, nil, rep@myco.com)", status, err, out.ID)
	}
	if _, err := Get(context.Background(), client, srv.URL, "tok", "/forbidden", nil, &out); !errors.Is(err, ErrAuthRejected) {
		t.Fatalf("Get /forbidden err = %v, want ErrAuthRejected", err)
	}
	if _, err := Get(context.Background(), client, srv.URL, "tok", "/boom", nil, &out); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("Get /boom err = %v, want ErrUnreachable", err)
	}
	if _, err := Get(context.Background(), client, srv.URL, "tok", "/garbage", nil, &out); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("Get /garbage (undecodable) err = %v, want ErrUnreachable", err)
	}
	// A 429 and a quota-403 are retryable rate limits, NOT rejected auth — the
	// registry must back off and honor Retry-After, not park the connection.
	var rl *connector.RateLimitedError
	if _, err := Get(context.Background(), client, srv.URL, "tok", "/toomany", nil, &out); !errors.As(err, &rl) {
		t.Fatalf("Get /toomany err = %v, want a RateLimitedError", err)
	} else if rl.RetryAfter != 30*time.Second {
		t.Errorf("RetryAfter = %v, want 30s", rl.RetryAfter)
	}
	rl = nil
	if _, err := Get(context.Background(), client, srv.URL, "tok", "/quota", nil, &out); !errors.As(err, &rl) {
		t.Fatalf("Get /quota (403 rateLimitExceeded) err = %v, want a RateLimitedError", err)
	}
}

func TestAuthenticateRejectsEmptyOwner(t *testing.T) {
	req, err := AuthRequestFrom("the-code", "https://app/callback")
	if err != nil {
		t.Fatalf("AuthRequestFrom: %v", err)
	}
	// A provider that returns a blank owner would make every counterparty look
	// external — refuse the connection rather than seal an unclassifiable one.
	owner := func(context.Context, string) (string, error) { return "  ", nil }
	if _, err := Authenticate(context.Background(), fakeOAuth{refresh: "r", access: "a"}, req, nil, owner); !errors.Is(err, ErrAuthRejected) {
		t.Fatalf("want ErrAuthRejected for an empty owner, got %v", err)
	}
}

func TestGetUnreachableHost(t *testing.T) {
	var out struct{}
	// A closed server → transport error → ErrUnreachable.
	srv := httptest.NewServer(http.NewServeMux())
	url := srv.URL
	srv.Close()
	if _, err := Get(context.Background(), srv.Client(), url, "tok", "/x", nil, &out); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("Get to a dead host err = %v, want ErrUnreachable", err)
	}
}
