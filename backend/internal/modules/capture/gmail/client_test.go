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
)

// googleStub routes the handful of Google/Gmail endpoints the client calls
// onto canned responses, so the REST/parse logic is proven with no network.
func googleStub(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			writeJSON(w, map[string]any{"access_token": "access-1", "refresh_token": "refresh-1", "expires_in": 3599})
		case "refresh_token":
			if r.Form.Get("refresh_token") != "refresh-1" {
				http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
				return
			}
			writeJSON(w, map[string]any{"access_token": "access-2", "expires_in": 3599})
		default:
			http.Error(w, "unsupported grant", http.StatusBadRequest)
		}
	})

	mux.HandleFunc("/profile", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"emailAddress": "rep@myco.com", "historyId": "12345"})
	})

	mux.HandleFunc("/messages", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"messages": []map[string]string{{"id": "m1"}, {"id": "m2"}}})
	})

	mux.HandleFunc("/messages/m1", func(w http.ResponseWriter, _ *http.Request) {
		raw := base64.RawURLEncoding.EncodeToString([]byte("Subject: hi\r\n\r\nbody"))
		writeJSON(w, map[string]any{"id": "m1", "raw": raw})
	})

	mux.HandleFunc("/history", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("startHistoryId") == "0" {
			// Google answers a too-old cursor with 404.
			http.Error(w, `{"error":{"code":404,"message":"historyId too old"}}`, http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]any{
			"history": []map[string]any{
				{"messagesAdded": []map[string]any{{"message": map[string]string{"id": "m3"}}}},
				{"messagesAdded": []map[string]any{{"message": map[string]string{"id": "m4"}}}},
			},
			"historyId": "99999",
		})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

//craft:ignore naked-any v is an arbitrary canned JSON response body for the stub
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	//craft:ignore swallowed-errors test stub write; an encode failure surfaces as the client-side decode error the assertion checks
	_ = json.NewEncoder(w).Encode(v)
}

func newTestClients(t *testing.T) (OAuth, API) {
	srv := googleStub(t)
	oauth := NewOAuth(OAuthConfig{
		ClientID:     "cid",
		ClientSecret: "secret",
		Scopes:       []string{"https://www.googleapis.com/auth/gmail.readonly"},
		AuthURL:      "https://accounts.example/auth",
		TokenURL:     srv.URL + "/token",
	})
	api := NewAPI(srv.Client(), srv.URL)
	return oauth, api
}

func TestAuthCodeURLCarriesStateAndOfflineConsent(t *testing.T) {
	oauth, _ := newTestClients(t)
	got := oauth.AuthCodeURL("state-xyz", "https://app/callback")
	for _, want := range []string{"state=state-xyz", "client_id=cid", "access_type=offline", "prompt=consent", "response_type=code"} {
		if !strings.Contains(got, want) {
			t.Errorf("authorize_url missing %q: %s", want, got)
		}
	}
}

func TestExchangeReturnsRefreshToken(t *testing.T) {
	oauth, _ := newTestClients(t)
	rt, err := oauth.Exchange(context.Background(), "the-code", "https://app/callback")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	if rt != "refresh-1" {
		t.Errorf("refresh token = %q, want refresh-1", rt)
	}
}

func TestAccessTokenRefreshes(t *testing.T) {
	oauth, _ := newTestClients(t)
	at, err := oauth.AccessToken(context.Background(), "refresh-1")
	if err != nil {
		t.Fatalf("AccessToken: %v", err)
	}
	if at != "access-2" {
		t.Errorf("access token = %q, want access-2", at)
	}
}

func TestAccessTokenRejectedRefreshMapsSentinel(t *testing.T) {
	oauth, _ := newTestClients(t)
	if _, err := oauth.AccessToken(context.Background(), "revoked"); !errors.Is(err, ErrAuthRejected) {
		t.Fatalf("want ErrAuthRejected for a revoked refresh, got %v", err)
	}
}

func TestProfileParsesEmailAndHistory(t *testing.T) {
	_, api := newTestClients(t)
	email, hist, err := api.Profile(context.Background(), "access-2")
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	if email != "rep@myco.com" || hist != "12345" {
		t.Errorf("Profile = (%q,%q), want (rep@myco.com,12345)", email, hist)
	}
}

func TestListRecentReturnsIDs(t *testing.T) {
	_, api := newTestClients(t)
	ids, err := api.ListRecent(context.Background(), "access-2", 50)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if len(ids) != 2 || ids[0] != "m1" || ids[1] != "m2" {
		t.Errorf("ids = %v, want [m1 m2]", ids)
	}
}

func TestGetRawDecodesBase64URL(t *testing.T) {
	_, api := newTestClients(t)
	raw, err := api.GetRaw(context.Background(), "access-2", "m1")
	if err != nil {
		t.Fatalf("GetRaw: %v", err)
	}
	if !strings.Contains(string(raw), "Subject: hi") {
		t.Errorf("decoded RFC822 = %q, want it to contain the header", raw)
	}
}

func TestHistoryCollectsAddedIDsAndAdvancesCursor(t *testing.T) {
	_, api := newTestClients(t)
	ids, hist, err := api.History(context.Background(), "access-2", "12345")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(ids) != 2 || ids[0] != "m3" || ids[1] != "m4" {
		t.Errorf("added ids = %v, want [m3 m4]", ids)
	}
	if hist != "99999" {
		t.Errorf("new historyId = %q, want 99999", hist)
	}
}

func TestHistoryTooOldMapsGoneSentinel(t *testing.T) {
	_, api := newTestClients(t)
	if _, _, err := api.History(context.Background(), "access-2", "0"); !errors.Is(err, ErrHistoryGone) {
		t.Fatalf("want ErrHistoryGone for a stale cursor, got %v", err)
	}
}
