// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The /mcp transport's own edge: the Origin allowlist and the three buckets
// that bound it. Each limit is asserted at its exact boundary against an
// ADVANCEABLE clock, so the numbers a deployment runs are the numbers under
// test and no assertion depends on wall-clock timing. The authorization
// server's ceilings are oauthedge_test.go; the clock and request helpers below
// are shared with it.

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// stepClock is a clock a test moves by hand. A fixed-window limiter's
// boundary is a property; sleeping to reach it would be slow and flaky at
// once.
type stepClock struct{ at time.Time }

func (c *stepClock) now() time.Time { return c.at }

func (c *stepClock) advance(d time.Duration) { c.at = c.at.Add(d) }

func newStepClock() *stepClock {
	return &stepClock{at: time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)}
}

// answering builds a handler that reports one fixed status, standing in for
// whatever the transport or the authorization server would have answered.
func answering(status int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) })
}

// mcpRequest builds one request at the edge: bearer empty sends NO
// Authorization header, which is the unauthenticated shape, not an empty
// credential.
func mcpRequest(method, bearer, remoteIP string) *http.Request {
	r := httptest.NewRequest(method, "/mcp", nil)
	r.RemoteAddr = remoteIP + ":51000"
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	return r
}

// serveStatus runs one request through a handler and reports the status.
func serveStatus(h http.Handler, r *http.Request) int {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec.Code
}

func TestOriginGuardAllowsAbsentAndRefusesForeign(t *testing.T) {
	cases := map[string]struct {
		origin string
		want   int
	}{
		// Non-browser clients send no Origin; refusing them would break every
		// CLI client. The real defence against rebinding is that every verb
		// needs a Bearer a rebound page cannot attach (DESIGN §5.3).
		"absent is allowed":     {"", http.StatusOK},
		"own origin is allowed": {"https://crm.example.com", http.StatusOK},
		"foreign origin is 403": {"https://evil.example", http.StatusForbidden},
		// A split dev stack serves the SPA from another loopback port.
		"loopback is allowed": {"http://localhost:5173", http.StatusOK},
		// A host that merely CONTAINS the allowed one is a foreign origin.
		"a lookalike host is 403": {"https://crm.example.com.evil.example", http.StatusForbidden},
		// An Origin no URL parser accepts is refused, not crashed on.
		"an unparseable Origin is 403": {"https://%zz", http.StatusForbidden},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			clock := newStepClock()
			edge := mcpEdge(answering(http.StatusOK), newMCPLimitersWithClock(clock.now), "https://crm.example.com")
			r := mcpRequest(http.MethodPost, "passport-token", "203.0.113.7")
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if got := serveStatus(edge, r); got != tc.want {
				t.Errorf("Origin %q → %d, want %d", tc.origin, got, tc.want)
			}
		})
	}
}

// TestPreAuthFailuresAreMeteredPerPresentedCredential pins the bucket the
// unauthenticated path had none of: presenting a credential that does not work
// costs a passport lookup, so repetition has to be bounded — and bounded on
// the credential, because the peer address is the front end's for every
// request in production and a budget keyed there is one bucket for everyone.
func TestPreAuthFailuresAreMeteredPerPresentedCredential(t *testing.T) {
	const grinder, innocent = "203.0.113.9", "198.51.100.4"
	clock := newStepClock()
	edge := mcpEdge(answering(http.StatusUnauthorized), newMCPLimitersWithClock(clock.now), "https://crm.example.com")

	for i := 1; i <= 60; i++ {
		got := serveStatus(edge, mcpRequest(http.MethodPost, "forged", grinder))
		if got != http.StatusUnauthorized {
			t.Fatalf("failure %d on one credential → %d, want 401 within the budget", i, got)
		}
	}
	if got := serveStatus(edge, mcpRequest(http.MethodPost, "forged", grinder)); got != http.StatusTooManyRequests {
		t.Fatalf("the 61st failure on one credential → %d, want 429", got)
	}
	// Another credential — even from the same peer — has its own budget, which
	// is the property that keeps a grinder from refusing everyone else.
	if got := serveStatus(edge, mcpRequest(http.MethodPost, "forged-other", grinder)); got != http.StatusUnauthorized {
		t.Fatalf("first failure on another credential → %d, want 401", got)
	}
	if got := serveStatus(edge, mcpRequest(http.MethodPost, "forged", innocent)); got != http.StatusTooManyRequests {
		t.Fatalf("the same spent credential from another peer → %d, want 429: the budget follows the credential", got)
	}
	clock.advance(time.Minute)
	if got := serveStatus(edge, mcpRequest(http.MethodPost, "forged", grinder)); got != http.StatusUnauthorized {
		t.Fatalf("after the window → %d, want the budget to have reopened (401)", got)
	}
}

// TestCredentiallessProbingIsBoundedPerPeer is the other arm of that key: a
// request carrying no Authorization header at all can only ever be a 401, so
// refusing it once its peer has spent the budget takes nothing away from a
// client that holds a working credential — and it keeps unauthenticated
// probing from being free.
func TestCredentiallessProbingIsBoundedPerPeer(t *testing.T) {
	const prober, innocent = "203.0.113.9", "198.51.100.4"
	clock := newStepClock()
	edge := mcpEdge(answering(http.StatusUnauthorized), newMCPLimitersWithClock(clock.now), "https://crm.example.com")

	for i := 1; i <= 60; i++ {
		if got := serveStatus(edge, mcpRequest(http.MethodPost, "", prober)); got != http.StatusUnauthorized {
			t.Fatalf("probe %d → %d, want 401 within the budget", i, got)
		}
	}
	if got := serveStatus(edge, mcpRequest(http.MethodPost, "", prober)); got != http.StatusTooManyRequests {
		t.Fatalf("the 61st credential-less probe → %d, want 429", got)
	}
	if got := serveStatus(edge, mcpRequest(http.MethodPost, "", innocent)); got != http.StatusUnauthorized {
		t.Fatalf("a credential-less probe from another peer → %d, want 401", got)
	}
}

// TestAFloodOfInvalidCredentialsCannotRefuseAValidOne is the availability
// property the whole keying exists for. In production TLS terminates ahead of
// this process, so every request — the attacker's and the real connector's —
// arrives from the front end's single address. A failure budget keyed there
// and consulted ahead of ALL traffic turns 60 junk bearers a minute into a
// total outage of the connector for every client of the installation.
func TestAFloodOfInvalidCredentialsCannotRefuseAValidOne(t *testing.T) {
	const frontEnd = "160.79.104.11"
	const valid = "the-live-passport"
	clock := newStepClock()
	// The transport answers 401 to anything but the one credential that
	// authenticates, which is what the real one does after its store lookup.
	edge := mcpEdge(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.Header.Get("Authorization"), valid) {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}), newMCPLimitersWithClock(clock.now), "https://crm.example.com")

	// Well past the 60/min failure budget, all from the one address every
	// request in this deployment shares.
	for i := 1; i <= 200; i++ {
		if got := serveStatus(edge, mcpRequest(http.MethodPost, "forged-"+strconv.Itoa(i), frontEnd)); got != http.StatusUnauthorized {
			t.Fatalf("forged bearer %d → %d, want 401", i, got)
		}
	}
	if got := serveStatus(edge, mcpRequest(http.MethodPost, valid, frontEnd)); got != http.StatusOK {
		t.Fatalf("the real connector's authenticated call → %d, want 200: a flood of invalid credentials must not deny service to a valid one", got)
	}
}

// TestAVaryingCredentialCannotEscapeThePreAuthCeiling is the other half of that
// key. The per-credential budget is keyed on what the caller presented, and the
// caller chooses it, so a grinder that varies the bearer meets that budget never
// — every forged value buying a fresh allowance and one indexed passport lookup.
// The per-peer failure ceiling is what it runs into instead.
//
// What that ceiling costs, and the test says it plainly: past 600 failures a
// minute from the front end's address a legitimate connector is refused too, for
// the remainder of that window. It sits there because a working connector
// produces no 401s at all.
func TestAVaryingCredentialCannotEscapeThePreAuthCeiling(t *testing.T) {
	const frontEnd, elsewhere = "160.79.104.11", "198.51.100.4"
	clock := newStepClock()
	edge := mcpEdge(answering(http.StatusUnauthorized), newMCPLimitersWithClock(clock.now), "https://crm.example.com")

	for i := 1; i <= 600; i++ {
		if got := serveStatus(edge, mcpRequest(http.MethodPost, "forged-"+strconv.Itoa(i), frontEnd)); got != http.StatusUnauthorized {
			t.Fatalf("forged bearer %d → %d, want 401 within the ceiling", i, got)
		}
	}
	if got := serveStatus(edge, mcpRequest(http.MethodPost, "forged-601", frontEnd)); got != http.StatusTooManyRequests {
		t.Fatalf("the 601st distinct forged bearer → %d, want 429: a varying credential is not a bypass", got)
	}
	// The ceiling is per peer, so it is not a lever on another peer's traffic.
	if got := serveStatus(edge, mcpRequest(http.MethodPost, "forged-602", elsewhere)); got != http.StatusUnauthorized {
		t.Fatalf("a forged bearer from another peer → %d, want 401", got)
	}
	clock.advance(time.Minute)
	if got := serveStatus(edge, mcpRequest(http.MethodPost, "forged-603", frontEnd)); got != http.StatusUnauthorized {
		t.Fatalf("after the window → %d, want the ceiling to have reopened (401)", got)
	}
}

// TestServedCallsNeverSpendThePeerFailureCeiling is what keeps the ceiling above
// from being the outage it replaces: it counts FAILURES, so no volume of served
// calls from the shared front-end address moves it, however many connectors sit
// behind that address.
func TestServedCallsNeverSpendThePeerFailureCeiling(t *testing.T) {
	const frontEnd = "160.79.104.11"
	clock := newStepClock()
	edge := mcpEdge(answering(http.StatusOK), newMCPLimitersWithClock(clock.now), "https://crm.example.com")

	// 600 served calls, spread over five connectors so none of them reaches its
	// own 240/min call budget — the volume a busy installation really produces.
	for i := 1; i <= 600; i++ {
		bearer := "passport-" + strconv.Itoa(i%5)
		if got := serveStatus(edge, mcpRequest(http.MethodPost, bearer, frontEnd)); got != http.StatusOK {
			t.Fatalf("served call %d → %d, want 200", i, got)
		}
	}
	if got := serveStatus(edge, mcpRequest(http.MethodPost, "forged", frontEnd)); got != http.StatusOK {
		t.Fatalf("a pre-auth attempt after 600 served calls → %d, want the failure ceiling untouched", got)
	}
}

// TestAuthenticatedCallsSpendOnlyTheirOwnPassportBudget proves the two
// buckets do not bleed into each other: a served call must not consume the
// pre-auth failure budget its shared egress IP holds, and one connector's
// volume must not throttle another's.
func TestAuthenticatedCallsSpendOnlyTheirOwnPassportBudget(t *testing.T) {
	const egress, other = "160.79.104.11", "160.79.104.12"
	clock := newStepClock()
	edge := mcpEdge(answering(http.StatusOK), newMCPLimitersWithClock(clock.now), "https://crm.example.com")

	for i := 1; i <= 240; i++ {
		if got := serveStatus(edge, mcpRequest(http.MethodPost, "passport-a", egress)); got != http.StatusOK {
			t.Fatalf("authenticated call %d → %d, want 200 within the budget", i, got)
		}
	}
	if got := serveStatus(edge, mcpRequest(http.MethodPost, "passport-a", egress)); got != http.StatusTooManyRequests {
		t.Fatalf("the 241st authenticated call → %d, want 429", got)
	}
	// Same egress range, different passport: the key is the credential, so
	// one busy connector cannot spend another's budget.
	if got := serveStatus(edge, mcpRequest(http.MethodPost, "passport-b", other)); got != http.StatusOK {
		t.Fatalf("a second passport → %d, want 200", got)
	}
	// 240 served calls from one IP cost nothing from the failure budget.
	if got := serveStatus(edge, mcpRequest(http.MethodPost, "", egress)); got != http.StatusOK {
		t.Fatalf("an unauthenticated call after 240 served ones → %d, want the pre-auth budget untouched", got)
	}
	clock.advance(time.Minute)
	if got := serveStatus(edge, mcpRequest(http.MethodPost, "passport-a", egress)); got != http.StatusOK {
		t.Fatalf("after the window → %d, want the budget to have reopened (200)", got)
	}
}

// TestStreamOpensGetTheirOwnTighterBudget pins the GET row: a stream is
// cheap to ask for and expensive to hold, so it is bounded separately from
// call volume — and it is metered whatever the transport answers a GET with
// today.
func TestStreamOpensGetTheirOwnTighterBudget(t *testing.T) {
	const peer = "160.79.104.11"
	clock := newStepClock()
	edge := mcpEdge(answering(http.StatusMethodNotAllowed), newMCPLimitersWithClock(clock.now), "https://crm.example.com")

	for i := 1; i <= 30; i++ {
		if got := serveStatus(edge, mcpRequest(http.MethodGet, "passport-a", peer)); got != http.StatusMethodNotAllowed {
			t.Fatalf("stream open %d → %d, want the transport's own answer within the budget", i, got)
		}
	}
	if got := serveStatus(edge, mcpRequest(http.MethodGet, "passport-a", peer)); got != http.StatusTooManyRequests {
		t.Fatalf("the 31st stream open → %d, want 429", got)
	}
	// Exhausting the stream budget must not cost the passport its calls.
	if got := serveStatus(edge, mcpRequest(http.MethodPost, "passport-a", peer)); got != http.StatusMethodNotAllowed {
		t.Fatalf("a call after the stream budget ran out → %d, want the call budget untouched", got)
	}
}

// TestMCPEdgePreservesResponseControllerCapabilities is a fitness function:
// the transport extends the write deadline for slow tool calls, which reaches
// the connection through http.NewResponseController. The status-capturing
// wrapper this edge adds must not hide it — the symptom of a missing Unwrap
// is a tool call that dies mid-response.
func TestMCPEdgePreservesResponseControllerCapabilities(t *testing.T) {
	clock := newStepClock()
	var deadlineErr, writeErr, flushErr error
	srv := httptest.NewServer(mcpEdge(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		rc := http.NewResponseController(w)
		deadlineErr = rc.SetWriteDeadline(time.Time{})
		_, writeErr = w.Write([]byte("x"))
		flushErr = rc.Flush()
	}), newMCPLimitersWithClock(clock.now), "https://crm.example.com"))
	defer srv.Close()

	resp, err := http.Post(srv.URL, "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST through the edge: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("closing the response body: %v", err)
	}
	if deadlineErr != nil {
		t.Errorf("SetWriteDeadline through the edge: %v", deadlineErr)
	}
	if writeErr != nil {
		t.Errorf("writing through the edge: %v", writeErr)
	}
	if flushErr != nil {
		t.Errorf("Flush through the edge: %v", flushErr)
	}
}

// TestMCPOriginAllowlistComesFromTheConfiguredResource: the guard compares
// scheme+host, so the "/mcp" path the resource document carries must be
// stripped — otherwise every browser Origin mismatches.
func TestMCPOriginAllowlistComesFromTheConfiguredResource(t *testing.T) {
	for _, tc := range []struct {
		resource, want string
	}{
		{"https://crm.example.com/mcp", "https://crm.example.com"},
		{"http://127.0.0.1:8080/mcp", "http://127.0.0.1:8080"},
		{"https://crm.example.com", "https://crm.example.com"},
		{"", ""},
		{"not-a-url", ""},
	} {
		if got := mcpOriginOf(tc.resource); got != tc.want {
			t.Errorf("mcpOriginOf(%q) = %q, want %q", tc.resource, got, tc.want)
		}
	}
}
