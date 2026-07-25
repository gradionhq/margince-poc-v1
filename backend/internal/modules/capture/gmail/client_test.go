// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package gmail

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/ports/connector"
)

// googleStub routes the handful of Google/Gmail endpoints the client calls
// onto canned responses, so the REST/parse logic is proven with no network.
func googleStub(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		// Google's token endpoint answers non-2xx on bad input; the client maps
		// any 4xx to ErrAuthRejected regardless of body, so a bare status is
		// all the stub needs (WriteHeader, not httperr — this fakes Google).
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
			writeJSON(w, map[string]any{"access_token": "access-2", "expires_in": 3599})
		default:
			w.WriteHeader(http.StatusBadRequest)
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

	// A message whose RAW body is larger than the old 8 MiB read cap — a phone
	// photo is enough. The response JSON exceeds 8 MiB, so a truncating reader
	// would fail to decode it.
	mux.HandleFunc("/messages/big", func(w http.ResponseWriter, _ *http.Request) {
		body := append([]byte("Subject: big\r\n\r\n"), bytes.Repeat([]byte("A"), 9<<20)...)
		writeJSON(w, map[string]any{"id": "big", "raw": base64.RawURLEncoding.EncodeToString(body)})
	})

	mux.HandleFunc("/watch", func(w http.ResponseWriter, _ *http.Request) {
		// Gmail's users.watch returns the mailbox historyId and an expiration
		// given as a string of milliseconds since the epoch.
		writeJSON(w, map[string]any{"historyId": "99999", "expiration": "1431990098200"})
	})

	mux.HandleFunc("/history", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("startHistoryId") == "0" {
			// Google answers a too-old cursor with 404 (mapped to ErrHistoryGone).
			w.WriteHeader(http.StatusNotFound)
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

// A 404 on GetRaw (the id vanished since it was listed) maps to the skip
// sentinel, not the reachability error — the pull skips it and continues.
// A large message (RAW body over the old 8 MiB read cap) must decode whole,
// not truncate — a single big email used to fail the JSON decode and wedge
// the entire pull as a spurious "unreachable".
func TestGetRawDecodesAMessageLargerThanEightMiB(t *testing.T) {
	_, api := newTestClients(t)
	raw, err := api.GetRaw(context.Background(), "access-2", "big")
	if err != nil {
		t.Fatalf("GetRaw on a >8 MiB message: %v", err)
	}
	if len(raw) < 9<<20 {
		t.Fatalf("decoded %d bytes, want the full body (~9 MiB) — the response was truncated", len(raw))
	}
	if !bytes.HasPrefix(raw, []byte("Subject: big")) {
		t.Fatalf("decoded body does not start with the header: %q", raw[:32])
	}
}

func TestGetRawMissingMessageMapsGoneSentinel(t *testing.T) {
	_, api := newTestClients(t)
	// The stub only serves /messages/m1; any other id 404s, exactly as Gmail
	// does for a message deleted between enumeration and fetch.
	_, err := api.GetRaw(context.Background(), "access-2", "m-vanished")
	if !errors.Is(err, ErrMessageGone) {
		t.Fatalf("GetRaw on a 404 = %v, want ErrMessageGone", err)
	}
	if !errors.Is(err, connector.ErrSkip) {
		t.Fatalf("ErrMessageGone must wrap connector.ErrSkip so the loops skip it, got %v", err)
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

func TestWatchRegistersAndParsesMillisecondExpiration(t *testing.T) {
	_, api := newTestClients(t)
	hist, exp, err := api.Watch(context.Background(), "access-2", "projects/p/topics/gmail-push")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if hist != "99999" {
		t.Errorf("historyId = %q, want 99999", hist)
	}
	// The string "1431990098200" ms must parse to that instant, not be truncated
	// to seconds or read as nanos.
	if want := time.UnixMilli(1431990098200); !exp.Equal(want) {
		t.Errorf("expiration = %v, want %v", exp, want)
	}
}

// Gmail runs its own transport, so the two facts the shared Google plumbing
// establishes have to hold here independently — otherwise the same provider
// failure is diagnosable on calendar and opaque on mail.
func TestGmailTransportCarriesGooglesReasonAndSharesTheQuotaVerdict(t *testing.T) {
	mux := http.NewServeMux()
	// The failure this whole change exists for: the API is not enabled for the
	// project. A 403, but no reconnect can clear it.
	mux.HandleFunc("/profile", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		//craft:ignore swallowed-errors test stub write
		_, _ = w.Write([]byte(`{"error":{"code":403,"errors":[{"reason":"accessNotConfigured"}]}}`))
	})
	// A daily-quota 403. It shares no substring with "rateLimitExceeded", so a
	// narrower predicate here would fall through to the auth arm and park a
	// merely-throttled mailbox as needing a human.
	mux.HandleFunc("/messages", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "42")
		w.WriteHeader(http.StatusForbidden)
		//craft:ignore swallowed-errors test stub write
		_, _ = w.Write([]byte(`{"error":{"code":403,"errors":[{"reason":"dailyLimitExceeded"}]}}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	api := NewAPI(srv.Client(), srv.URL)

	_, _, err := api.Profile(context.Background(), "access")
	if !errors.Is(err, ErrAuthRejected) {
		t.Fatalf("disabled-API err = %v, want ErrAuthRejected", err)
	}
	if got := connector.ProviderReason(err); got != "accessNotConfigured" {
		t.Errorf("ProviderReason = %q, want accessNotConfigured", got)
	}

	_, err = api.ListRecent(context.Background(), "access", 10)
	if errors.Is(err, ErrAuthRejected) {
		t.Fatalf("a daily-quota 403 was classified as rejected auth: %v", err)
	}
	rl, ok := errors.AsType[*connector.RateLimitedError](err)
	if !ok {
		t.Fatalf("quota err = %v, want a RateLimitedError so the sync backs off", err)
	}
	if rl.RetryAfter != 42*time.Second {
		t.Errorf("RetryAfter = %v, want 42s from the provider header", rl.RetryAfter)
	}
}

// users.watch renewal is a Gmail call like any other, so a throttled renewal
// must read as throttling and carry the provider's Retry-After — not as a
// refused credential with the delay thrown away. (The renewal's own failure is
// logged and skipped rather than parking the connection; what is wrong is the
// verdict, and the next caller to trust it.)
func TestWatchTreatsThrottlingAsThrottlingNotRejectedAuth(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"429":              {http.StatusTooManyRequests, `{}`},
		"403 rate limited": {http.StatusForbidden, `{"error":{"errors":[{"reason":"rateLimitExceeded"}]}}`},
		"403 daily quota":  {http.StatusForbidden, `{"error":{"errors":[{"reason":"dailyLimitExceeded"}]}}`},
		"403 newer enum":   {http.StatusForbidden, `{"error":{"details":[{"reason":"RATE_LIMIT_EXCEEDED"}]}}`},
	} {
		t.Run(name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("/watch", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Retry-After", "13")
				w.WriteHeader(tc.status)
				//craft:ignore swallowed-errors test stub write
				_, _ = w.Write([]byte(tc.body))
			})
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)

			_, _, err := NewAPI(srv.Client(), srv.URL).Watch(context.Background(), "access", "projects/p/topics/t")

			if errors.Is(err, ErrAuthRejected) {
				t.Fatalf("throttled watch classified as rejected auth: %v", err)
			}
			rl, ok := errors.AsType[*connector.RateLimitedError](err)
			if !ok {
				t.Fatalf("err = %v, want a RateLimitedError so the renewal backs off", err)
			}
			if rl.RetryAfter != 13*time.Second {
				t.Errorf("RetryAfter = %v, want 13s from the provider header", rl.RetryAfter)
			}
		})
	}
}

// A 403 that does NOT name a limit stays a refused credential. The quota check
// reads the parsed reason precisely for this: a body merely mentioning a limit in
// prose must not keep a revoked grant alive and retrying forever.
func TestForbiddenWithoutALimitReasonStaysRejectedAuth(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/profile", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		//craft:ignore swallowed-errors test stub write
		_, _ = w.Write([]byte(`{"error":{"errors":[{"reason":"authError","message":"quotaExceeded is not why"}]}}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	_, _, err := NewAPI(srv.Client(), srv.URL).Profile(context.Background(), "access")

	if _, ok := errors.AsType[*connector.RateLimitedError](err); ok {
		t.Fatalf("prose mentioning a quota literal was read as throttling: %v", err)
	}
	if !errors.Is(err, ErrAuthRejected) {
		t.Errorf("err = %v, want ErrAuthRejected so the connection is parked", err)
	}
}
