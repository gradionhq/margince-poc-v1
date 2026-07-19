// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package graph

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
	"github.com/gradionhq/margince/backend/internal/shared/ports/datasource"
)

// faultySink returns a fixed error from Upsert, to drive captureOne's
// Sink-error (fatal) and Sink-ErrSkip (counted skip) branches.
type faultySink struct{ err error }

func (f faultySink) Upsert(context.Context, connector.NormalizedRecord) (datasource.EntityRef, error) {
	return datasource.EntityRef{}, f.err
}

// realConn builds a connector over the REAL OAuth+API clients pointed at the
// given stub, so a test drives client.go and graph.go together (the error
// paths neither the pure-fake connector test nor the client test alone hit).
func realConn(t *testing.T, mux *serveMuxWithBase) *Connector {
	t.Helper()
	oauth := NewOAuth(OAuthConfig{ClientID: "cid", ClientSecret: "sec", TokenURL: mux.srv.URL + "/token"})
	return New(oauth, NewAPI(mux.srv.Client(), mux.srv.URL))
}

// serveMuxWithBase couples a ServeMux with the httptest server serving it, so
// stub handlers can mint absolute continuation links on their own origin.
type serveMuxWithBase struct {
	mux *http.ServeMux
	srv *httptest.Server
}

// fullStub answers every endpoint the connect+sync path touches; any entry in
// overrides replaces the default (http.ServeMux forbids registering the same
// pattern twice, so overrides are applied at registration).
func fullStub(t *testing.T, overrides map[string]http.HandlerFunc) *serveMuxWithBase {
	t.Helper()
	s := &serveMuxWithBase{mux: http.NewServeMux()}
	reg := func(pat string, def http.HandlerFunc) {
		if h, ok := overrides[pat]; ok {
			s.mux.HandleFunc(pat, h)
			return
		}
		s.mux.HandleFunc(pat, def)
	}
	reg("/token", func(w http.ResponseWriter, r *http.Request) {
		//craft:ignore swallowed-errors test stub; ParseForm on the recorded request can't fail
		_ = r.ParseForm()
		body := map[string]any{"access_token": "at", "expires_in": 3599}
		if r.Form.Get("grant_type") == "authorization_code" {
			body["refresh_token"] = "rt"
		}
		writeJSON(w, body)
	})
	reg("/me", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"mail": "rep@myco.com"})
	})
	reg("/me/mailFolders/inbox/messages/delta", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"value":            []map[string]any{{"id": "m1"}},
			"@odata.deltaLink": s.srv.URL + "/me/mailFolders/inbox/messages/delta?%24deltatoken=d1",
		})
	})
	reg("/me/mailFolders/sentitems/messages/delta", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"value":            []map[string]any{},
			"@odata.deltaLink": s.srv.URL + "/me/mailFolders/sentitems/messages/delta?%24deltatoken=s1",
		})
	})
	reg("/me/messages/m1/$value", func(w http.ResponseWriter, _ *http.Request) {
		//craft:ignore swallowed-errors test stub write; a short write surfaces as the client-side assertion failure
		_, _ = w.Write(rawMsg("m1@x", "a@acme.com"))
	})
	s.srv = httptest.NewServer(s.mux)
	t.Cleanup(s.srv.Close)
	return s
}

func fail500(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }

func authViaStub(t *testing.T, c *Connector) []byte {
	t.Helper()
	req, err := AuthRequestFrom("code", "https://app/cb")
	if err != nil {
		t.Fatalf("AuthRequestFrom: %v", err)
	}
	auth, err := c.Authenticate(context.Background(), req)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	return auth
}

func TestAuthenticateThenInitialSyncThroughRealClient(t *testing.T) {
	c := realConn(t, fullStub(t, nil))
	auth := authViaStub(t, c)

	sink := &recordingSink{}
	cur, err := c.Sync(context.Background(), auth, nil, sink)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(sink.recs) != 1 || sink.recs[0].Source != "graph:m1@x" {
		t.Fatalf("want one graph:m1@x record, got %+v", sink.recs)
	}
	if delta, _ := parseCursor(cur); delta.DeltaLink == "" {
		t.Error("cursor deltaLink should be anchored after the initial sync")
	}
}

func TestSyncSinkSkipIsCountedAndErrorPropagates(t *testing.T) {
	// The Sink deliberately skips (e.g. suppression) → counted, not fatal.
	skipC := realConn(t, fullStub(t, nil))
	skipAuth := authViaStub(t, skipC)
	if _, err := skipC.Sync(context.Background(), skipAuth, nil, faultySink{err: connector.ErrSkip}); err != nil {
		t.Fatalf("a Sink ErrSkip must not fail the pull: %v", err)
	}

	// A real Sink write fault stops the pull.
	errC := realConn(t, fullStub(t, nil))
	errAuth := authViaStub(t, errC)
	wantErr := errors.New("db down")
	if _, err := errC.Sync(context.Background(), errAuth, nil, faultySink{err: wantErr}); !errors.Is(err, wantErr) {
		t.Fatalf("a Sink write fault should propagate, got %v", err)
	}
}

func TestHealthCheckOKAndFailure(t *testing.T) {
	c := realConn(t, fullStub(t, nil))
	auth := authViaStub(t, c)
	if err := c.HealthCheck(context.Background(), auth); err != nil {
		t.Fatalf("HealthCheck ok path: %v", err)
	}

	// Build the auth directly (profile is broken, so Authenticate can't run):
	// HealthCheck still mints a token, then its own profile call 500s.
	bad := realConn(t, fullStub(t, map[string]http.HandlerFunc{"/me": fail500}))
	if err := bad.HealthCheck(context.Background(), authBytes(t)); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("HealthCheck against a 500 profile = %v, want ErrUnreachable", err)
	}
}

func TestAuthenticateRejectedWhenNoRefreshToken(t *testing.T) {
	s := fullStub(t, map[string]http.HandlerFunc{"/token": func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"access_token": "at"}) // no refresh_token
	}})
	c := realConn(t, s)
	req, err := AuthRequestFrom("code", "https://app/cb")
	if err != nil {
		t.Fatalf("AuthRequestFrom: %v", err)
	}
	if _, err := c.Authenticate(context.Background(), req); !errors.Is(err, ErrAuthRejected) {
		t.Fatalf("Authenticate with no refresh token = %v, want ErrAuthRejected", err)
	}
}

func TestSyncStopsOnGetMIMEFault(t *testing.T) {
	c := realConn(t, fullStub(t, map[string]http.HandlerFunc{"/me/messages/m1/$value": fail500}))
	auth := authViaStub(t, c)
	if _, err := c.Sync(context.Background(), auth, nil, &recordingSink{}); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("Sync with a failing GetMIME = %v, want ErrUnreachable", err)
	}
}

func TestSyncSkipsAutomatedMail(t *testing.T) {
	auto := func(w http.ResponseWriter, _ *http.Request) {
		//craft:ignore swallowed-errors test stub write; a short write surfaces as the client-side assertion failure
		_, _ = w.Write([]byte("From: no-reply@x.com\r\nTo: rep@myco.com\r\nSubject: hi\r\nMessage-ID: <a@x>\r\nContent-Type: text/plain\r\n\r\nx"))
	}
	c := realConn(t, fullStub(t, map[string]http.HandlerFunc{"/me/messages/m1/$value": auto}))
	auth := authViaStub(t, c)
	sink := &recordingSink{}
	if _, err := c.Sync(context.Background(), auth, nil, sink); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(sink.recs) != 0 {
		t.Fatalf("automated mail should be skipped, but %d record(s) landed", len(sink.recs))
	}
}
