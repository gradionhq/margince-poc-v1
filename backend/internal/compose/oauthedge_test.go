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

	"github.com/gradionhq/margince/backend/internal/modules/identity"
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

// registerAt builds one dynamic client registration from remoteIP.
func registerAt(remoteIP string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, oauthRegisterPath, strings.NewReader(`{"client_name":"c"}`))
	r.RemoteAddr = remoteIP + ":51000"
	return r
}

// TestRegistrationSurvivesRealVolumeAndItsDenialDoesNotOutliveTheFlood pins the
// one arm with no per-caller key at all: dynamic client registration is
// anonymous, so the only key is the peer — which behind a TLS-terminating front
// end is every caller on earth. The ceiling is therefore set above any real
// volume and windowed by the minute.
//
// It replaces a test asserting the 11th registration from one IP is refused for
// the rest of the HOUR. That assertion pinned the defect twice over: ten
// unauthenticated requests bought an hour of installation-wide registration
// outage, and Claude registers a fresh client on every connection, so the 11th
// human to connect within the hour was refused too.
func TestRegistrationSurvivesRealVolumeAndItsDenialDoesNotOutliveTheFlood(t *testing.T) {
	const frontEnd, elsewhere = "160.79.104.11", "198.51.100.4"
	clock := newStepClock()
	edge := oauthEdge(answering(http.StatusCreated), newMCPLimitersWithClock(clock.now))

	// Far more fresh connections in one minute than an installation makes, all
	// from the single address every request shares in production.
	for i := 1; i <= 60; i++ {
		if got := serveStatus(edge, registerAt(frontEnd)); got != http.StatusCreated {
			t.Fatalf("registration %d → %d, want 201: real connection volume must not be refused", i, got)
		}
	}
	// A ceiling still exists — the alternative to a cheap outage is not
	// unbounded row creation by an unauthenticated caller.
	if got := serveStatus(edge, registerAt(frontEnd)); got != http.StatusTooManyRequests {
		t.Fatalf("the 61st registration inside one minute → %d, want 429", got)
	}
	if got := serveStatus(edge, registerAt(elsewhere)); got != http.StatusCreated {
		t.Fatalf("a registration from another peer → %d, want 201", got)
	}
	// The window is a MINUTE: a flood denies registration while it is running
	// and not for an hour after it stops.
	clock.advance(time.Minute)
	if got := serveStatus(edge, registerAt(frontEnd)); got != http.StatusCreated {
		t.Fatalf("a minute after the flood → %d, want 201: the denial must not outlive the flood", got)
	}
}

// consentRequest builds one consent-flow request. session empty sends NO cookie,
// which is the shape of a caller with no live session — every such request is
// refused by the middleware behind this edge, so it may only ever spend the
// per-peer arm of the budget.
func consentRequest(method, session, remoteIP string) *http.Request {
	r := httptest.NewRequest(method, oauthAuthorizePath+"?client_id=c", nil)
	r.RemoteAddr = remoteIP + ":51000"
	if session != "" {
		r.AddCookie(&http.Cookie{Name: identity.SessionCookieName, Value: session}) // #nosec G124 -- request-side cookie: AddCookie sends name=value only
	}
	return r
}

// TestASessionlessFloodCannotDenyASignedInHumanConsent is the availability
// property the consent key exists for. Consent requires a live session, so a
// caller with none can never complete it — and must therefore never be able to
// spend the budget of a human who has one.
//
// It replaces a test asserting the 61st request on ANY /oauth path from one IP
// is a 429 off one shared per-IP budget. That is the defect stated as a
// requirement: behind the front end this repo documents, one request per second
// from anywhere refused every human's consent installation-wide, and
// /oauth/revoke sat in the same shared budget.
func TestASessionlessFloodCannotDenyASignedInHumanConsent(t *testing.T) {
	const frontEnd = "160.79.104.11"
	clock := newStepClock()
	edge := oauthEdge(answering(http.StatusOK), newMCPLimitersWithClock(clock.now))

	for i := 1; i <= 60; i++ {
		if got := serveStatus(edge, consentRequest(http.MethodGet, "", frontEnd)); got != http.StatusOK {
			t.Fatalf("session-less consent attempt %d → %d, want the middleware's own answer within the budget", i, got)
		}
	}
	if got := serveStatus(edge, consentRequest(http.MethodGet, "", frontEnd)); got != http.StatusTooManyRequests {
		t.Fatalf("the 61st session-less attempt → %d, want 429: probing without a session is bounded per peer", got)
	}
	// The property: the flood above spent the peer's arm, and a human who is
	// actually signed in is unaffected by it — GET form and POST grant alike.
	if got := serveStatus(edge, consentRequest(http.MethodGet, "marcus-session", frontEnd)); got != http.StatusOK {
		t.Fatalf("a signed-in human's consent form after the flood → %d, want 200", got)
	}
	if got := serveStatus(edge, consentRequest(http.MethodPost, "marcus-session", frontEnd)); got != http.StatusOK {
		t.Fatalf("a signed-in human's grant after the flood → %d, want 200", got)
	}
	// And one human's own volume is not spendable against another's: marcus has
	// spent two of his sixty above, so 58 more exhaust his bucket and no one
	// else's.
	for i := 3; i <= 60; i++ {
		if got := serveStatus(edge, consentRequest(http.MethodGet, "marcus-session", frontEnd)); got != http.StatusOK {
			t.Fatalf("marcus's consent attempt %d → %d, want 200 within his own budget", i, got)
		}
	}
	if got := serveStatus(edge, consentRequest(http.MethodGet, "marcus-session", frontEnd)); got != http.StatusTooManyRequests {
		t.Fatalf("marcus's 61st attempt → %d, want 429", got)
	}
	if got := serveStatus(edge, consentRequest(http.MethodGet, "priya-session", frontEnd)); got != http.StatusOK {
		t.Fatalf("a second human's consent form → %d, want 200: the budget is per presented session", got)
	}
}

// TestVaryingTheSessionCookieCannotEscapeTheConsentCeiling is the other half of
// that key: the cookie arrives on the wire, so a caller can vary it, and a
// per-session bucket alone would hand every fresh value a fresh allowance. The
// per-peer ceiling is what a rotating cookie runs into.
func TestVaryingTheSessionCookieCannotEscapeTheConsentCeiling(t *testing.T) {
	const grinder, elsewhere = "203.0.113.9", "198.51.100.4"
	clock := newStepClock()
	edge := oauthEdge(answering(http.StatusOK), newMCPLimitersWithClock(clock.now))

	for i := 1; i <= 600; i++ {
		if got := serveStatus(edge, consentRequest(http.MethodGet, "forged-"+strconv.Itoa(i), grinder)); got != http.StatusOK {
			t.Fatalf("consent attempt %d under a fresh cookie → %d, want 200 within the ceiling", i, got)
		}
	}
	if got := serveStatus(edge, consentRequest(http.MethodGet, "forged-601", grinder)); got != http.StatusTooManyRequests {
		t.Fatalf("the 601st attempt under yet another fresh cookie → %d, want 429: a varying cookie is not a bypass", got)
	}
	if got := serveStatus(edge, consentRequest(http.MethodGet, "marcus-session", elsewhere)); got != http.StatusOK {
		t.Fatalf("a consent form from another peer → %d, want 200: the ceiling is per peer", got)
	}
	clock.advance(time.Minute)
	if got := serveStatus(edge, consentRequest(http.MethodGet, "forged-602", grinder)); got != http.StatusOK {
		t.Fatalf("after the window → %d, want the ceiling to have reopened (200)", got)
	}
}

// revokeRequest builds one RFC 7009 revocation of token from remoteIP.
func revokeRequest(token, remoteIP string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, oauthRevokePath, strings.NewReader("token="+token))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.RemoteAddr = remoteIP + ":51000"
	return r
}

// TestRevocationIsReachableWhateverElseTheEdgeIsRefusing: the kill switch's
// availability may not be spendable by anyone else's traffic. Every other arm is
// driven to its ceiling from the one address production shares, and revocation
// still answers — then its own key is shown to be the presented token, so
// repetition is bounded without one client's retries touching another's.
func TestRevocationIsReachableWhateverElseTheEdgeIsRefusing(t *testing.T) {
	const frontEnd = "160.79.104.11"
	clock := newStepClock()
	edge := oauthEdge(answering(http.StatusOK), newMCPLimitersWithClock(clock.now))

	// Setup, not assertion: every other arm is driven past its ceiling from the
	// one address production shares, which is what the assertion below has to
	// survive.
	unlisted := httptest.NewRequest(http.MethodGet, "/oauth/introspect", nil)
	unlisted.RemoteAddr = frontEnd + ":51000"
	for i := 1; i <= 601; i++ {
		serveStatus(edge, consentRequest(http.MethodGet, "forged-"+strconv.Itoa(i), frontEnd))
		serveStatus(edge, tokenRequest("client-"+strconv.Itoa(i), frontEnd))
		serveStatus(edge, registerAt(frontEnd))
		serveStatus(edge, unlisted)
	}
	if got := serveStatus(edge, revokeRequest("mgp_the-clients-own-token", frontEnd)); got != http.StatusOK {
		t.Fatalf("revocation while every other arm is refusing → %d, want 200: consent and mint traffic must not spend the kill switch", got)
	}
	for i := 1; i <= 59; i++ {
		if got := serveStatus(edge, revokeRequest("mgp_the-clients-own-token", frontEnd)); got != http.StatusOK {
			t.Fatalf("revocation %d of one token → %d, want 200 within the budget", i+1, got)
		}
	}
	if got := serveStatus(edge, revokeRequest("mgp_the-clients-own-token", frontEnd)); got != http.StatusTooManyRequests {
		t.Fatalf("the 61st revocation of the SAME token → %d, want 429", got)
	}
	if got := serveStatus(edge, revokeRequest("mgp_another-clients-token", frontEnd)); got != http.StatusOK {
		t.Fatalf("revoking a different token from the same peer → %d, want 200: the budget follows the presented token", got)
	}
}

// TestAnUnlistedOAuthPathArrivesBoundedInItsOwnGroup pins the default arm: a
// route this router grows later is limited rather than unlimited, and its
// traffic — hostile or not — spends neither consent's budget nor revocation's.
func TestAnUnlistedOAuthPathArrivesBoundedInItsOwnGroup(t *testing.T) {
	const frontEnd = "160.79.104.11"
	clock := newStepClock()
	edge := oauthEdge(answering(http.StatusOK), newMCPLimitersWithClock(clock.now))
	introspect := func() int {
		r := httptest.NewRequest(http.MethodGet, "/oauth/introspect", nil)
		r.RemoteAddr = frontEnd + ":51000"
		return serveStatus(edge, r)
	}

	for i := 1; i <= 600; i++ {
		if got := introspect(); got != http.StatusOK {
			t.Fatalf("unlisted-path request %d → %d, want the router's own answer within the ceiling", i, got)
		}
	}
	if got := introspect(); got != http.StatusTooManyRequests {
		t.Fatalf("the 601st request on an unlisted path → %d, want 429", got)
	}
	if got := serveStatus(edge, consentRequest(http.MethodGet, "marcus-session", frontEnd)); got != http.StatusOK {
		t.Fatalf("consent after an unlisted path's flood → %d, want 200", got)
	}
	if got := serveStatus(edge, revokeRequest("mgp_the-clients-own-token", frontEnd)); got != http.StatusOK {
		t.Fatalf("revocation after an unlisted path's flood → %d, want 200", got)
	}
}
