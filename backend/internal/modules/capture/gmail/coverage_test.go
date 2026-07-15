// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package gmail

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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

// realConn builds a connector over the REAL OAuth+API clients pointed at the
// given stub, so a test drives client.go and gmail.go together (the error
// paths neither the pure-fake connector test nor the client test alone hit).
func realConn(t *testing.T, mux *http.ServeMux) *Connector {
	t.Helper()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	oauth := NewOAuth(OAuthConfig{ClientID: "cid", ClientSecret: "sec", TokenURL: srv.URL + "/token"})
	return New(oauth, NewAPI(srv.Client(), srv.URL))
}

// fullStub answers every endpoint the connect+backfill path touches; any
// entry in overrides replaces the default (http.ServeMux forbids registering
// the same pattern twice, so overrides are applied at registration).
func fullStub(t *testing.T, overrides map[string]http.HandlerFunc) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	reg := func(pat string, def http.HandlerFunc) {
		if h, ok := overrides[pat]; ok {
			mux.HandleFunc(pat, h)
			return
		}
		mux.HandleFunc(pat, def)
	}
	reg("/token", func(w http.ResponseWriter, r *http.Request) {
		//craft:ignore swallowed-errors test stub; ParseForm on the recorded request can't fail
		_ = r.ParseForm()
		body := map[string]any{"access_token": "at", "expires_in": 3599}
		if r.Form.Get("grant_type") == "authorization_code" {
			body["refresh_token"] = "rt"
		}
		writeStub(w, body)
	})
	reg("/profile", func(w http.ResponseWriter, _ *http.Request) {
		writeStub(w, map[string]any{"emailAddress": "rep@myco.com", "historyId": "500"})
	})
	reg("/messages", func(w http.ResponseWriter, _ *http.Request) {
		writeStub(w, map[string]any{"messages": []map[string]string{{"id": "m1"}}})
	})
	reg("/messages/m1", func(w http.ResponseWriter, _ *http.Request) {
		writeStub(w, map[string]any{"raw": base64.RawURLEncoding.EncodeToString(rawMsg("m1@x", "a@acme.com"))})
	})
	return mux
}

//craft:ignore naked-any v is an arbitrary canned JSON response body for the stub
func writeStub(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	//craft:ignore swallowed-errors test stub write; an encode failure surfaces as the client-side decode error the assertion checks
	_ = json.NewEncoder(w).Encode(v)
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

func TestAuthenticateThenBackfillThroughRealClient(t *testing.T) {
	c := realConn(t, fullStub(t, nil))
	auth := authViaStub(t, c)

	sink := &recordingSink{}
	cur, err := c.Sync(context.Background(), auth, nil, sink)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(sink.recs) != 1 || sink.recs[0].Source != "gmail:m1@x" {
		t.Fatalf("want one gmail:m1@x record, got %+v", sink.recs)
	}
	if hid, _ := parseCursor(cur); hid != "500" {
		t.Errorf("cursor = %q, want 500", hid)
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
	bad := realConn(t, fullStub(t, map[string]http.HandlerFunc{"/profile": fail500}))
	badAuth := authBytes(t)
	if err := bad.HealthCheck(context.Background(), badAuth); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("HealthCheck against a 500 profile = %v, want ErrUnreachable", err)
	}
}

func TestAuthenticateRejectedWhenNoRefreshToken(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, _ *http.Request) {
		writeStub(w, map[string]any{"access_token": "at"}) // no refresh_token
	})
	c := realConn(t, mux)
	req, _ := AuthRequestFrom("code", "https://app/cb")
	if _, err := c.Authenticate(context.Background(), req); !errors.Is(err, ErrAuthRejected) {
		t.Fatalf("Authenticate with no refresh token = %v, want ErrAuthRejected", err)
	}
}

func TestSyncStopsOnGetRawFault(t *testing.T) {
	c := realConn(t, fullStub(t, map[string]http.HandlerFunc{"/messages/m1": fail500}))
	auth := authViaStub(t, c)
	if _, err := c.Sync(context.Background(), auth, nil, &recordingSink{}); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("Sync with a failing GetRaw = %v, want ErrUnreachable", err)
	}
}

func TestSyncSkipsAutomatedMail(t *testing.T) {
	auto := func(w http.ResponseWriter, _ *http.Request) {
		msg := "From: no-reply@x.com\r\nTo: rep@myco.com\r\nSubject: hi\r\nMessage-ID: <a@x>\r\nContent-Type: text/plain\r\n\r\nx"
		writeStub(w, map[string]any{"raw": base64.RawURLEncoding.EncodeToString([]byte(msg))})
	}
	c := realConn(t, fullStub(t, map[string]http.HandlerFunc{"/messages/m1": auto}))
	auth := authViaStub(t, c)
	sink := &recordingSink{}
	if _, err := c.Sync(context.Background(), auth, nil, sink); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(sink.recs) != 0 {
		t.Fatalf("automated mail should be skipped, but %d record(s) landed", len(sink.recs))
	}
}

func TestClientUnreachableWhenServerDown(t *testing.T) {
	srv := httptest.NewServer(http.NewServeMux())
	srv.Close() // nothing will answer
	api := NewAPI(srv.Client(), srv.URL)
	if _, _, err := api.Profile(context.Background(), "at"); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("Profile against a closed server = %v, want ErrUnreachable", err)
	}
}

func TestGetRawRejectsBadBase64(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/messages/m1", func(w http.ResponseWriter, _ *http.Request) {
		writeStub(w, map[string]any{"raw": "!!!not base64!!!"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	if _, err := NewAPI(srv.Client(), srv.URL).GetRaw(context.Background(), "at", "m1"); err == nil {
		t.Fatal("GetRaw should reject undecodable base64")
	}
}

func TestHistoryFollowsPagination(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/history", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("pageToken") == "" {
			writeStub(w, map[string]any{
				"history":       []map[string]any{{"messagesAdded": []map[string]any{{"message": map[string]string{"id": "p1"}}}}},
				"nextPageToken": "tok2",
				"historyId":     "10",
			})
			return
		}
		writeStub(w, map[string]any{
			"history":   []map[string]any{{"messagesAdded": []map[string]any{{"message": map[string]string{"id": "p2"}}}}},
			"historyId": "20",
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	ids, hist, err := NewAPI(srv.Client(), srv.URL).History(context.Background(), "at", "1")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if strings.Join(ids, ",") != "p1,p2" || hist != "20" {
		t.Fatalf("pagination: ids=%v hist=%q, want [p1 p2] / 20", ids, hist)
	}
}
