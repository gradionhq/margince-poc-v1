// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package graph

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// msStub routes the handful of Microsoft identity/Graph endpoints the client
// calls onto canned responses, so the REST/parse logic is proven with no
// network. Delta continuation links are rewritten onto the stub's own origin
// at request time (the client refuses off-origin links by design).
func msStub(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var srv *httptest.Server

	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		// Microsoft's token endpoint answers non-2xx on bad input; the client
		// maps any 4xx to ErrAuthRejected regardless of body, so a bare status
		// is all the stub needs (WriteHeader, not httperr — this fakes Microsoft).
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			writeJSON(w, map[string]any{"access_token": "access-1", "refresh_token": "refresh-1", "expires_in": 3599})
		case "refresh_token":
			if r.Form.Get("refresh_token") != "refresh-1" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			writeJSON(w, map[string]any{"access_token": "access-2", "refresh_token": "refresh-2", "expires_in": 3599})
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	})

	mux.HandleFunc("/me", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"mail": "rep@myco.com", "userPrincipalName": "rep@myco.onmicrosoft.com"})
	})

	mux.HandleFunc("/me/mailFolders/inbox/messages/delta", func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("$deltatoken") {
		case "gone":
			// Graph answers an expired delta state with 410 Gone.
			w.WriteHeader(http.StatusGone)
		case "d1":
			writeJSON(w, map[string]any{
				"value":            []map[string]any{{"id": "m3"}, {"id": "tombstone", "@removed": map[string]string{"reason": "deleted"}}},
				"@odata.deltaLink": srv.URL + "/me/mailFolders/inbox/messages/delta?%24deltatoken=d2",
			})
		default:
			// The opening page of a fresh delta round: one page, then the next.
			if r.URL.Query().Get("$skiptoken") == "p2" {
				writeJSON(w, map[string]any{
					"value":            []map[string]any{{"id": "m2"}},
					"@odata.deltaLink": srv.URL + "/me/mailFolders/inbox/messages/delta?%24deltatoken=d1",
				})
				return
			}
			writeJSON(w, map[string]any{
				"value":           []map[string]any{{"id": "m1"}},
				"@odata.nextLink": srv.URL + "/me/mailFolders/inbox/messages/delta?%24skiptoken=p2",
			})
		}
	})

	mux.HandleFunc("/me/messages", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("$count") == "true" {
			if r.Header.Get("ConsistencyLevel") != "eventual" {
				// $count without the header fails on real Graph; the stub makes
				// that contract visible as a client-side failure.
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			writeJSON(w, map[string]any{"@odata.count": 4200, "value": []map[string]any{{"id": "m1"}}})
			return
		}
		if r.URL.Query().Get("$skiptoken") == "p2" {
			writeJSON(w, map[string]any{"value": []map[string]any{{"id": "m2"}}})
			return
		}
		writeJSON(w, map[string]any{
			"value":           []map[string]any{{"id": "m1"}},
			"@odata.nextLink": srv.URL + "/me/messages?%24skiptoken=p2",
		})
	})

	mux.HandleFunc("/me/messages/m1/$value", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "message/rfc822")
		//craft:ignore swallowed-errors test stub write; a short write surfaces as the client-side assertion failure
		_, _ = w.Write([]byte("Subject: hi\r\n\r\nbody"))
	})

	mux.HandleFunc("/me/messages/throttled/$value", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "17")
		w.WriteHeader(http.StatusTooManyRequests)
	})

	mux.HandleFunc("/me/messages/huge/$value", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "message/rfc822")
		// One byte past the 8 MiB cap — an oversized message.
		//craft:ignore swallowed-errors test stub write; a short write surfaces as the client-side assertion
		_, _ = w.Write(make([]byte, (8<<20)+1))
	})

	srv = httptest.NewServer(mux)
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
	srv := msStub(t)
	oauth := NewOAuth(OAuthConfig{
		ClientID:     "cid",
		ClientSecret: "secret",
		Scopes:       []string{"offline_access", "User.Read", "Mail.Read"},
		AuthURL:      "https://login.example/auth",
		TokenURL:     srv.URL + "/token",
	})
	api := NewAPI(srv.Client(), srv.URL)
	return oauth, api
}

func TestAuthCodeURLCarriesStateAndScopes(t *testing.T) {
	oauth, _ := newTestClients(t)
	got := oauth.AuthCodeURL("state-xyz", "https://app/callback")
	for _, want := range []string{"state=state-xyz", "client_id=cid", "offline_access", "Mail.Read", "response_type=code"} {
		if !strings.Contains(got, want) {
			t.Errorf("authorize_url missing %q: %s", want, got)
		}
	}
}

func TestDefaultEndpointsUseTheConfiguredTenant(t *testing.T) {
	got := NewOAuth(OAuthConfig{ClientID: "cid", Tenant: "contoso.example"}).AuthCodeURL("s", "https://app/cb")
	if !strings.HasPrefix(got, "https://login.microsoftonline.com/contoso.example/oauth2/v2.0/authorize?") {
		t.Errorf("tenant endpoint = %q, want the contoso.example authorize URL", got)
	}
	common := NewOAuth(OAuthConfig{ClientID: "cid"}).AuthCodeURL("s", "https://app/cb")
	if !strings.HasPrefix(common, "https://login.microsoftonline.com/common/oauth2/v2.0/authorize?") {
		t.Errorf("default endpoint = %q, want the common authorize URL", common)
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

func TestProfilePrefersMailOverUPN(t *testing.T) {
	_, api := newTestClients(t)
	email, err := api.Profile(context.Background(), "access-2")
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	if email != "rep@myco.com" {
		t.Errorf("Profile = %q, want the mail attribute over userPrincipalName", email)
	}
}

func TestProfileFallsBackToUPN(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/me", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"userPrincipalName": "rep@myco.onmicrosoft.com"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	email, err := NewAPI(srv.Client(), srv.URL).Profile(context.Background(), "at")
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	if email != "rep@myco.onmicrosoft.com" {
		t.Errorf("Profile = %q, want the userPrincipalName fallback", email)
	}
}

func TestDeltaInitWalksPagesFiltersTombstonesAndReturnsDeltaLink(t *testing.T) {
	_, api := newTestClients(t)
	ids, delta, err := api.DeltaInit(context.Background(), "access-2", time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("DeltaInit: %v", err)
	}
	if strings.Join(ids, ",") != "m1,m2" {
		t.Errorf("ids = %v, want [m1 m2] across both pages", ids)
	}
	if !strings.Contains(delta, "%24deltatoken=d1") && !strings.Contains(delta, "$deltatoken=d1") {
		t.Errorf("deltaLink = %q, want the d1 link that closes the round", delta)
	}
}

func TestDeltaResumeCollectsAddedAndSkipsRemoved(t *testing.T) {
	srv := msStub(t)
	api := NewAPI(srv.Client(), srv.URL)
	ids, delta, err := api.Delta(context.Background(), "access-2", srv.URL+"/me/mailFolders/inbox/messages/delta?%24deltatoken=d1")
	if err != nil {
		t.Fatalf("Delta: %v", err)
	}
	if strings.Join(ids, ",") != "m3" {
		t.Errorf("ids = %v, want [m3] (the tombstoned entry is not fetched)", ids)
	}
	if !strings.Contains(delta, "d2") {
		t.Errorf("advanced deltaLink = %q, want the d2 link", delta)
	}
}

func TestDeltaGoneMapsCursorSentinel(t *testing.T) {
	srv := msStub(t)
	api := NewAPI(srv.Client(), srv.URL)
	if _, _, err := api.Delta(context.Background(), "access-2", srv.URL+"/me/mailFolders/inbox/messages/delta?%24deltatoken=gone"); !errors.Is(err, ErrDeltaGone) {
		t.Fatalf("want ErrDeltaGone for a 410 delta, got %v", err)
	}
}

func TestDeltaRefusesOffOriginLink(t *testing.T) {
	srv := msStub(t)
	api := NewAPI(srv.Client(), srv.URL)
	if _, _, err := api.Delta(context.Background(), "access-2", "https://attacker.example/steal-token"); err == nil {
		t.Fatal("Delta must refuse a deltaLink that points off the Graph API")
	}
}

func TestGetMIMEReturnsRawBytes(t *testing.T) {
	_, api := newTestClients(t)
	raw, err := api.GetMIME(context.Background(), "access-2", "m1")
	if err != nil {
		t.Fatalf("GetMIME: %v", err)
	}
	if !strings.Contains(string(raw), "Subject: hi") {
		t.Errorf("MIME = %q, want it to contain the header", raw)
	}
}

func TestGetMIMERefusesOversizedMessage(t *testing.T) {
	_, api := newTestClients(t)
	_, err := api.GetMIME(context.Background(), "access-2", "huge")
	if !errors.Is(err, connector.ErrSkip) {
		t.Fatalf("an oversized message must be a skip, not truncated capture, got %v", err)
	}
}

func TestThrottledCallMapsRateLimitWithRetryAfter(t *testing.T) {
	_, api := newTestClients(t)
	_, err := api.GetMIME(context.Background(), "access-2", "throttled")
	if !errors.Is(err, connector.ErrRateLimited) {
		t.Fatalf("want ErrRateLimited for a 429, got %v", err)
	}
	var rl *connector.RateLimitedError
	if !errors.As(err, &rl) || rl.RetryAfter != 17*time.Second {
		t.Errorf("RetryAfter = %v, want 17s from the provider header", rl)
	}
}

func TestEstimateAfterReadsODataCount(t *testing.T) {
	_, api := newTestClients(t)
	n, err := api.EstimateAfter(context.Background(), "access-2", time.Date(2026, 1, 18, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("EstimateAfter: %v", err)
	}
	if n != 4200 {
		t.Errorf("estimate = %d, want 4200 (@odata.count)", n)
	}
}

func TestListAfterPagesViaNextLink(t *testing.T) {
	_, api := newTestClients(t)
	ids, next, err := api.ListAfter(context.Background(), "access-2", time.Date(2026, 1, 18, 0, 0, 0, 0, time.UTC), "", 100)
	if err != nil {
		t.Fatalf("ListAfter: %v", err)
	}
	if strings.Join(ids, ",") != "m1" || next == "" {
		t.Fatalf("first page = %v next=%q, want [m1] and a nextLink", ids, next)
	}
	ids2, next2, err := api.ListAfter(context.Background(), "access-2", time.Time{}, next, 100)
	if err != nil {
		t.Fatalf("ListAfter page 2: %v", err)
	}
	if strings.Join(ids2, ",") != "m2" || next2 != "" {
		t.Errorf("second page = %v next=%q, want [m2] and the end of the walk", ids2, next2)
	}
}

func TestListAfterRefusesOffOriginToken(t *testing.T) {
	_, api := newTestClients(t)
	if _, _, err := api.ListAfter(context.Background(), "access-2", time.Time{}, "https://attacker.example/page", 100); err == nil {
		t.Fatal("ListAfter must refuse a page token that points off the Graph API")
	}
}

func TestClientUnreachableWhenServerDown(t *testing.T) {
	srv := httptest.NewServer(http.NewServeMux())
	srv.Close() // nothing will answer
	api := NewAPI(srv.Client(), srv.URL)
	if _, err := api.Profile(context.Background(), "at"); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("Profile against a closed server = %v, want ErrUnreachable", err)
	}
}
