// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The authorization server's own edge: the ceilings on the token endpoint, on
// dynamic client registration and on the consent flow, plus the body-handling
// the token key depends on. Split from mcpedge_test.go — which keeps the /mcp
// transport's guards — so each file is one surface; the shared clock and
// request helpers live there.

import (
	"crypto/sha256"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// tokenRequest builds one form-encoded authorization-code exchange.
func tokenRequest(clientID, remoteIP string) *http.Request {
	body := "grant_type=authorization_code&code=abc123&client_id=" + clientID
	r := httptest.NewRequest(http.MethodPost, oauthTokenPath, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.RemoteAddr = remoteIP + ":51000"
	return r
}

// TestTokenRequestsAreMeteredPerClientAndIP is the reason the key is a pair:
// all claude.ai traffic arrives from one published egress range, so an
// IP-only bucket on the token endpoint would be one ceiling for the whole
// installation and one busy client would lock out every other.
func TestTokenRequestsAreMeteredPerClientAndIP(t *testing.T) {
	const egress, elsewhere = "160.79.104.11", "198.51.100.4"
	clock := newStepClock()
	edge := oauthEdge(answering(http.StatusOK), newMCPLimitersWithClock(clock.now))

	for i := 1; i <= 60; i++ {
		if got := serveStatus(edge, tokenRequest("client-a", egress)); got != http.StatusOK {
			t.Fatalf("token exchange %d → %d, want 200 within the budget", i, got)
		}
	}
	if got := serveStatus(edge, tokenRequest("client-a", egress)); got != http.StatusTooManyRequests {
		t.Fatalf("the 61st exchange for one client → %d, want 429", got)
	}
	if got := serveStatus(edge, tokenRequest("client-b", egress)); got != http.StatusOK {
		t.Fatalf("another client behind the SAME egress IP → %d, want 200: the bucket is (client_id, IP)", got)
	}
	if got := serveStatus(edge, tokenRequest("client-a", elsewhere)); got != http.StatusOK {
		t.Fatalf("the same client from another IP → %d, want 200: the bucket is (client_id, IP)", got)
	}
}

// TestVaryingTheClientIDCannotEscapeTheTokenCeiling is the other half of that
// pair: client_id comes out of the request body, so the caller picks it, and a
// per-(client, IP) bucket alone hands a fresh allowance to every fresh value —
// no bound at all on the endpoint that mints passports. The per-IP ceiling is
// what a rotating client_id runs into.
func TestVaryingTheClientIDCannotEscapeTheTokenCeiling(t *testing.T) {
	const grinder, elsewhere = "203.0.113.9", "198.51.100.4"
	clock := newStepClock()
	edge := oauthEdge(answering(http.StatusOK), newMCPLimitersWithClock(clock.now))

	for i := 1; i <= 600; i++ {
		if got := serveStatus(edge, tokenRequest("client-"+strconv.Itoa(i), grinder)); got != http.StatusOK {
			t.Fatalf("exchange %d under a fresh client_id → %d, want 200 within the budget", i, got)
		}
	}
	if got := serveStatus(edge, tokenRequest("client-601", grinder)); got != http.StatusTooManyRequests {
		t.Fatalf("the 601st exchange under yet another fresh client_id → %d, want 429: a varying client_id is not a bypass", got)
	}
	// The ceiling is per peer, so it is not a lever on anyone else.
	if got := serveStatus(edge, tokenRequest("client-a", elsewhere)); got != http.StatusOK {
		t.Fatalf("an exchange from another peer → %d, want 200", got)
	}
	clock.advance(time.Minute)
	if got := serveStatus(edge, tokenRequest("client-602", grinder)); got != http.StatusOK {
		t.Fatalf("after the window → %d, want the budget to have reopened (200)", got)
	}
}

// A client_id longer than any this server issues must not become a long-lived
// map key: the limiter retains keys for up to two windows, so an unbounded key
// is a memory sink an unauthenticated caller can drive. The key is a digest, so
// two oversized values are still two buckets — and still bounded ones.
func TestTokenBucketKeyIsBoundedWhateverTheClientIDLength(t *testing.T) {
	const ip = "203.0.113.9"
	oversized := strings.Repeat("p", tokenFormPeek)

	key := tokenBucketKey(oversized, ip)
	if len(key) != sha256.Size*2+1+len(ip) {
		t.Errorf("key for a %d-char client_id is %d chars, want a fixed-length digest plus the peer", len(oversized), len(key))
	}
	if key == tokenBucketKey(oversized+"x", ip) {
		t.Error("two different client_ids share one bucket, so the per-client ceiling is not per client")
	}
}

// TestTokenEdgeLeavesTheFormBodyReadable is the other half of that key:
// reading client_id means reading the body, and the handler behind the edge
// parses the same body. A drained body would turn every token exchange into a
// 400 — the whole handshake, broken by the limiter that keys it.
func TestTokenEdgeLeavesTheFormBodyReadable(t *testing.T) {
	clock := newStepClock()
	var parsed string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("the handler cannot parse the form the client sent: %v", err)
			return
		}
		parsed = r.PostForm.Get("grant_type") + " " + r.PostForm.Get("code") + " " + r.PostForm.Get("client_id")
		w.WriteHeader(http.StatusOK)
	})
	edge := oauthEdge(handler, newMCPLimitersWithClock(clock.now))

	if got := serveStatus(edge, tokenRequest("client-a", "160.79.104.11")); got != http.StatusOK {
		t.Fatalf("token exchange → %d, want 200", got)
	}
	if want := "authorization_code abc123 client-a"; parsed != want {
		t.Errorf("the handler read %q from the body, want %q", parsed, want)
	}
}

// TestTokenRequestWithNoReadableClientIDKeepsTheIPHalf: a body the edge
// cannot read a client_id out of shares its IP's bucket. That is the previous
// ceiling, not an escape from it — and the body still reaches the handler that
// has to answer for it.
func TestTokenRequestWithNoReadableClientIDKeepsTheIPHalf(t *testing.T) {
	for _, tc := range []struct{ name, contentType, body string }{
		{"a JSON body is not a form", "application/json", `{"grant_type":"authorization_code"}`},
		{"a form body with a broken escape", "application/x-www-form-urlencoded", "client_id=%zz"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := newStepClock()
			var seen string
			edge := oauthEdge(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("the handler cannot read the body: %v", err)
					return
				}
				seen = string(raw)
				w.WriteHeader(http.StatusOK)
			}), newMCPLimitersWithClock(clock.now))
			ask := func() int {
				r := httptest.NewRequest(http.MethodPost, oauthTokenPath, strings.NewReader(tc.body))
				r.Header.Set("Content-Type", tc.contentType)
				r.RemoteAddr = "160.79.104.11:51000"
				return serveStatus(edge, r)
			}

			for i := 1; i <= 60; i++ {
				if got := ask(); got != http.StatusOK {
					t.Fatalf("exchange %d → %d, want 200 within the budget", i, got)
				}
			}
			if got := ask(); got != http.StatusTooManyRequests {
				t.Fatalf("the 61st exchange → %d, want 429: an unreadable client_id is not a bypass", got)
			}
			if seen != tc.body {
				t.Errorf("the handler read %q, want the body the client sent (%q)", seen, tc.body)
			}
		})
	}
}

// TestOversizedTokenBodyStillReachesTheHandlerWhole: a body past the peek cap
// is the handler's error to answer, so the edge must hand it on intact rather
// than truncate it into a different request.
func TestOversizedTokenBodyStillReachesTheHandlerWhole(t *testing.T) {
	clock := newStepClock()
	padding := strings.Repeat("p", tokenFormPeek)
	body := "grant_type=authorization_code&client_id=client-a&code_verifier=" + padding
	var read int
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("the handler cannot read the body: %v", err)
			return
		}
		read = len(raw)
		w.WriteHeader(http.StatusBadRequest)
	})
	edge := oauthEdge(handler, newMCPLimitersWithClock(clock.now))

	r := httptest.NewRequest(http.MethodPost, oauthTokenPath, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.RemoteAddr = "160.79.104.11:51000"
	if got := serveStatus(edge, r); got != http.StatusBadRequest {
		t.Fatalf("oversized token body → %d, want the handler's own answer", got)
	}
	if read != len(body) {
		t.Errorf("the handler read %d bytes of a %d-byte body: the edge truncated the request", read, len(body))
	}
}

// TestRegistrationIsMeteredPerIPPerHour pins the tightest row: dynamic client
// registration creates rows for an unauthenticated caller.
func TestRegistrationIsMeteredPerIPPerHour(t *testing.T) {
	const peer, elsewhere = "203.0.113.9", "198.51.100.4"
	clock := newStepClock()
	edge := oauthEdge(answering(http.StatusCreated), newMCPLimitersWithClock(clock.now))
	register := func(remoteIP string) int {
		r := httptest.NewRequest(http.MethodPost, oauthRegisterPath, strings.NewReader(`{"client_name":"c"}`))
		r.RemoteAddr = remoteIP + ":51000"
		return serveStatus(edge, r)
	}

	for i := 1; i <= 10; i++ {
		if got := register(peer); got != http.StatusCreated {
			t.Fatalf("registration %d → %d, want 201 within the budget", i, got)
		}
	}
	if got := register(peer); got != http.StatusTooManyRequests {
		t.Fatalf("the 11th registration from one IP → %d, want 429", got)
	}
	if got := register(elsewhere); got != http.StatusCreated {
		t.Fatalf("a registration from another IP → %d, want 201", got)
	}
	// An hour, not a minute: a minute later the door is still shut.
	clock.advance(time.Minute)
	if got := register(peer); got != http.StatusTooManyRequests {
		t.Fatalf("a minute later → %d, want 429: the registration window is an hour", got)
	}
	clock.advance(time.Hour)
	if got := register(peer); got != http.StatusCreated {
		t.Fatalf("an hour later → %d, want 201", got)
	}
}

// TestAuthorizeAndAnyOtherOAuthPathShareThePerIPBudget pins both the
// authorize row and the default arm: the consent form and the grant are two
// halves of one human flow, and a path added to the authorization server
// without a row of its own arrives limited rather than unlimited.
func TestAuthorizeAndAnyOtherOAuthPathShareThePerIPBudget(t *testing.T) {
	const peer = "203.0.113.9"
	clock := newStepClock()
	edge := oauthEdge(answering(http.StatusOK), newMCPLimitersWithClock(clock.now))
	ask := func(method, path string) int {
		r := httptest.NewRequest(method, path, nil)
		r.RemoteAddr = peer + ":51000"
		return serveStatus(edge, r)
	}

	for i := 1; i <= 30; i++ {
		if got := ask(http.MethodGet, "/oauth/authorize?client_id=c"); got != http.StatusOK {
			t.Fatalf("consent form %d → %d, want 200 within the budget", i, got)
		}
		if got := ask(http.MethodPost, "/oauth/authorize"); got != http.StatusOK {
			t.Fatalf("grant %d → %d, want 200 within the budget", i, got)
		}
	}
	// The 61st request on that shared budget — and an unlisted path proves
	// the default arm is what refuses it.
	if got := ask(http.MethodGet, "/oauth/introspect"); got != http.StatusTooManyRequests {
		t.Fatalf("an unlisted authorization-server path past the budget → %d, want 429", got)
	}
}
