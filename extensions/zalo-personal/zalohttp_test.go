// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalopersonal

// The jar IS the session. Zalo hands out the real `zpw_sek` on chat.zalo.me
// while simultaneously CLEARING a same-named cookie at the broader `.zalo.me`
// scope, so a jar that keeps cleared cookies sends two `zpw_sek` values and the
// server rejects the request with error_code 102 — which reads as "your session
// expired", never as "your cookie jar is wrong".
//
// This file also holds the two seams every other test in the package drives:
// [fakeClock], so nothing reads the real clock, and [routeTo], so every call
// this package makes reaches an httptest.Server instead of Zalo.

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// fixedTime is the instant every test starts from. Any time is fine; a fixed
// one is what makes an expiry assertion mean something.
var fixedTime = time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)

// fakeClock advances by a fixed step on every read, which is what lets a
// budgeted long-poll loop terminate without anything sleeping.
type fakeClock struct {
	at   time.Time
	step time.Duration
}

func newFakeClock(step time.Duration) *fakeClock {
	return &fakeClock{at: fixedTime, step: step}
}

func (c *fakeClock) now() time.Time {
	c.at = c.at.Add(c.step)
	return c.at
}

// routeTo sends every request this package makes to one httptest.Server,
// leaving the request's own URL — and therefore the cookie scoping — as Zalo
// would see it.
type routeTo struct {
	host string
}

func (r routeTo) RoundTrip(req *http.Request) (*http.Response, error) {
	proxied := req.Clone(req.Context())
	proxied.URL.Scheme = "http"
	proxied.URL.Host = r.host
	return http.DefaultTransport.RoundTrip(proxied)
}

// testOptions wires a package call chain to a test server and a fake clock.
func testOptions(t *testing.T, srv *httptest.Server, step time.Duration) zaloOptions {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	return zaloOptions{Transport: routeTo{host: u.Host}, Now: newFakeClock(step).now}
}

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u
}

func testJar() *jar {
	return newJar(func() time.Time { return fixedTime })
}

func cookieValues(t *testing.T, j *jar, rawURL, name string) []string {
	t.Helper()
	var out []string
	for _, c := range j.Cookies(mustParse(t, rawURL)) {
		if c.Name == name {
			out = append(out, c.Value)
		}
	}
	return out
}

// TestClearedCookieDoesNotShadowTheLiveOne: a broad-scope clear must not
// survive to be sent alongside the host-scoped credential it shadows.
func TestClearedCookieDoesNotShadowTheLiveOne(t *testing.T) {
	j := testJar()

	j.SetCookies(mustParse(t, "https://chat.zalo.me/index.html"), []*http.Cookie{
		{Name: "zpw_sek", Value: "the-real-session-key", Domain: "chat.zalo.me", Path: "/"},
	})
	// The same response clears the sibling at the parent domain. Zalo spells
	// this three ways; all three mean "forget it".
	j.SetCookies(mustParse(t, "https://chat.zalo.me/index.html"), []*http.Cookie{
		{Name: "zpw_sek", Value: "EXPIRED", Domain: ".zalo.me", Path: "/", MaxAge: -1},
		{Name: "zsid", Value: "EXPIRED", Domain: ".zalo.me", Path: "/", Expires: fixedTime.Add(-time.Hour)},
		{Name: "zpw_enk", Value: "", Domain: ".zalo.me", Path: "/"},
	})

	got := cookieValues(t, j, "https://chat.zalo.me/api", "zpw_sek")
	if len(got) != 1 || got[0] != "the-real-session-key" {
		t.Fatalf("zpw_sek sent to chat.zalo.me = %q, want exactly [the-real-session-key]", got)
	}
	if v := cookieValues(t, j, "https://chat.zalo.me/api", "zsid"); len(v) != 0 {
		t.Fatalf("expired zsid was still sent: %q", v)
	}
	if v := cookieValues(t, j, "https://chat.zalo.me/api", "zpw_enk"); len(v) != 0 {
		t.Fatalf("emptied zpw_enk was still sent: %q", v)
	}
}

// TestMostSpecificDomainIsSentFirst pins the order RFC 6265 asks for. Two
// legitimately co-existing scopes are allowed; which one the server reads first
// must not depend on Go's map iteration.
func TestMostSpecificDomainIsSentFirst(t *testing.T) {
	j := testJar()
	j.SetCookies(mustParse(t, "https://zalo.me/"), []*http.Cookie{
		{Name: "zpsid", Value: "broad", Domain: ".zalo.me", Path: "/"},
	})
	j.SetCookies(mustParse(t, "https://chat.zalo.me/"), []*http.Cookie{
		{Name: "zpsid", Value: "specific", Domain: "chat.zalo.me", Path: "/"},
	})

	for i := 0; i < 20; i++ {
		got := cookieValues(t, j, "https://chat.zalo.me/api", "zpsid")
		if len(got) != 2 || got[0] != "specific" || got[1] != "broad" {
			t.Fatalf("attempt %d: zpsid order = %q, want [specific broad]", i, got)
		}
	}
}

// TestACookieIsOnlySentToItsOwnDomainSuffix keeps the suffix match from being
// a substring match: `evilzalo.me` must not receive `.zalo.me` cookies.
func TestACookieIsOnlySentToItsOwnDomainSuffix(t *testing.T) {
	j := testJar()
	j.SetCookies(mustParse(t, "https://zalo.me/"), []*http.Cookie{
		{Name: "zpsid", Value: "broad", Domain: ".zalo.me", Path: "/"},
	})

	if got := cookieValues(t, j, "https://evilzalo.me/steal", "zpsid"); len(got) != 0 {
		t.Fatalf("a .zalo.me cookie was sent to evilzalo.me: %q", got)
	}
	if got := cookieValues(t, j, "https://deep.chat.zalo.me/api", "zpsid"); len(got) != 1 {
		t.Fatalf("a .zalo.me cookie did not reach a sub-subdomain: %q", got)
	}
}

// TestACookieWithNoDomainIsScopedToTheHostThatSetIt covers the id.zalo.me hops,
// which set their bootstrap cookies without a Domain attribute.
func TestACookieWithNoDomainIsScopedToTheHostThatSetIt(t *testing.T) {
	j := testJar()
	j.SetCookies(mustParse(t, "https://id.zalo.me/account"), []*http.Cookie{
		{Name: "zlogin", Value: "bootstrap"},
	})

	if got := cookieValues(t, j, "https://id.zalo.me/account/logininfo", "zlogin"); len(got) != 1 {
		t.Fatalf("host-scoped cookie was not sent back to its own host: %q", got)
	}
	if got := cookieValues(t, j, "https://chat.zalo.me/api", "zlogin"); len(got) != 0 {
		t.Fatalf("host-scoped cookie leaked to a sibling host: %q", got)
	}
}

// TestTheJarRoundTripsThroughTheSealedCookieList is what makes a credential
// survive a process restart: export then load has to reproduce what the jar
// sends, and a cleared cookie must not come back to life through the blob.
func TestTheJarRoundTripsThroughTheSealedCookieList(t *testing.T) {
	origin := testJar()
	origin.SetCookies(mustParse(t, "https://chat.zalo.me/"), []*http.Cookie{
		{Name: "zpw_sek", Value: "live", Domain: "chat.zalo.me", Path: "/"},
		{Name: "zsid", Value: "EXPIRED", Domain: ".zalo.me", Path: "/", MaxAge: -1},
	})
	origin.SetCookies(mustParse(t, "https://zalo.me/"), []*http.Cookie{
		{Name: "zpsid", Value: "broad", Domain: ".zalo.me", Path: "/"},
	})

	sealed := origin.export()
	for _, c := range sealed {
		if c.Name == "zsid" {
			t.Fatalf("a cleared cookie was exported into the sealed credential: %+v", c)
		}
	}

	restored := testJar()
	restored.load(sealed)
	// A blob written before the jar honoured deletion still holds clears; they
	// must not be resurrected on load either.
	restored.load([]zaloCookie{{Name: "zsid", Value: "", Domain: ".zalo.me", Path: "/"}})

	want := cookieValues(t, origin, "https://chat.zalo.me/api", "zpsid")
	if got := cookieValues(t, restored, "https://chat.zalo.me/api", "zpsid"); len(got) != 1 || got[0] != want[0] {
		t.Fatalf("restored jar sends zpsid %q, want %q", got, want)
	}
	if got := cookieValues(t, restored, "https://chat.zalo.me/api", "zsid"); len(got) != 0 {
		t.Fatalf("a cleared cookie was resurrected on load: %q", got)
	}
}

// TestExportIsStableSoAnUnchangedCredentialLooksUnchanged: a blob whose bytes
// churn on every read looks like a rotated credential to whatever stores it.
func TestExportIsStableSoAnUnchangedCredentialLooksUnchanged(t *testing.T) {
	j := testJar()
	j.SetCookies(mustParse(t, "https://chat.zalo.me/"), []*http.Cookie{
		{Name: "b", Value: "1", Domain: "chat.zalo.me"},
		{Name: "a", Value: "2", Domain: "chat.zalo.me"},
		{Name: "c", Value: "3", Domain: ".zalo.me"},
	})

	first := j.export()
	for i := 0; i < 20; i++ {
		next := j.export()
		if len(next) != len(first) {
			t.Fatalf("export length changed between reads: %d then %d", len(first), len(next))
		}
		for k := range first {
			if next[k] != first[k] {
				t.Fatalf("export order changed between reads at %d: %+v then %+v", k, first[k], next[k])
			}
		}
	}
}

func TestMakeURLStampsTheVersionPairOnlyWhenAsked(t *testing.T) {
	withVersion, err := makeURL("https://wpa.chat.zalo.me/api/x", map[string]string{"a": "1"}, true)
	if err != nil {
		t.Fatalf("make url: %v", err)
	}
	if withVersion != "https://wpa.chat.zalo.me/api/x?a=1&zpw_type=30&zpw_ver=689" {
		t.Errorf("versioned URL = %q", withVersion)
	}

	// getServerInfo is the endpoint that must NOT carry the pair.
	bare, err := makeURL("https://wpa.chat.zalo.me/api/x", map[string]string{"a": "1"}, false)
	if err != nil {
		t.Fatalf("make url: %v", err)
	}
	if bare != "https://wpa.chat.zalo.me/api/x?a=1" {
		t.Errorf("unversioned URL = %q", bare)
	}

	if _, err := makeURL("://not a url", nil, true); err == nil {
		t.Error("an unparseable base produced a URL")
	}
}

// TestTheIMEIBindsTheUserAgent is the guard on the comment at defaultUserAgent:
// the device identity ends in MD5(userAgent), so the agent string is credential
// material and a "tidy-up" of the constant silently invalidates every session.
func TestTheIMEIBindsTheUserAgent(t *testing.T) {
	const other = "Mozilla/5.0 (something else)"

	first, err := newIMEI(defaultUserAgent)
	if err != nil {
		t.Fatalf("mint imei: %v", err)
	}
	second, err := newIMEI(defaultUserAgent)
	if err != nil {
		t.Fatalf("mint imei: %v", err)
	}
	changed, err := newIMEI(other)
	if err != nil {
		t.Fatalf("mint imei: %v", err)
	}

	suffix := func(imei string) string { return imei[len(imei)-32:] }
	if suffix(first) != suffix(second) {
		t.Error("two imeis for the same user agent do not share the agent digest")
	}
	if suffix(first) == suffix(changed) {
		t.Error("changing the user agent did not change the imei — the identity is not bound to it")
	}
	if first == second {
		t.Error("two imeis for the same user agent are identical; the device half is not random")
	}
}

// TestATransportFailureIsTellableFromAnAnswer is what SendText's
// outcome-unknown distinction rests on.
func TestATransportFailureIsTellableFromAnAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		if _, err := w.Write([]byte("upstream is down")); err != nil {
			t.Errorf("write test response: %v", err)
		}
	}))
	defer srv.Close()

	c := newClient(testOptions(t, srv, time.Second))
	_, err := c.doJSON(t.Context(), http.MethodGet, "https://chat.zalo.me/api/x", nil, nil)

	var transport *transportError
	if err == nil || !errors.As(err, &transport) {
		t.Fatalf("a non-200 answer produced %v, want a transportError", err)
	}
	if transport.URL != "https://chat.zalo.me/api/x" {
		t.Errorf("transportError names %q, want the URL that was called", transport.URL)
	}
}

func TestTheTwoErrorKindsSayWhichOneHappened(t *testing.T) {
	underlying := errors.New("connection reset")
	transport := &transportError{Method: http.MethodPost, URL: "https://chat.zalo.me/api/message/sms", Err: underlying}
	if !strings.Contains(transport.Error(), "did not complete") {
		t.Errorf("transportError reads %q, which does not say the outcome is unknown", transport)
	}
	if !errors.Is(transport, underlying) {
		t.Error("transportError does not unwrap to what actually failed")
	}

	refusal := &refusalError{Endpoint: "waiting-confirm", Code: -13, Message: "declined"}
	got := refusal.Error()
	for _, want := range []string{"waiting-confirm", "-13", "declined"} {
		if !strings.Contains(got, want) {
			t.Errorf("refusalError reads %q, which omits %q", got, want)
		}
	}
}

// TestARedirectChainCarriesTheRefererZaloExpects: the login chain hops between
// id.zalo.me and chat.zalo.me, and Zalo serves a challenge page rather than a
// session to a hop whose referer it does not recognise.
func TestARedirectChainCarriesTheRefererZaloExpects(t *testing.T) {
	// The handler runs on net/http's goroutine and this test reads what it
	// recorded on its own; the response crossing a socket is not a
	// happens-before edge the memory model gives us, so the observation is
	// guarded. An unguarded fake reports a race in the code under test.
	var (
		mu               sync.Mutex
		refererOnLastHop string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/hop" {
			http.Redirect(w, r, "/landed", http.StatusFound)
			return
		}
		mu.Lock()
		refererOnLastHop = r.Header.Get("Referer")
		mu.Unlock()
		if _, err := w.Write([]byte(`{"error_code":0}`)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer srv.Close()

	c := newClient(testOptions(t, srv, time.Second))
	if _, err := c.doJSON(t.Context(), http.MethodGet, "https://id.zalo.me/hop", nil, nil); err != nil {
		t.Fatalf("follow redirect: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if refererOnLastHop != "https://id.zalo.me/" {
		t.Errorf("referer after the hop = %q, want id.zalo.me", refererOnLastHop)
	}
}

func TestAnEndlessRedirectLoopStopsRatherThanSpins(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/again", http.StatusFound)
	}))
	defer srv.Close()

	c := newClient(testOptions(t, srv, time.Second))
	_, err := c.doJSON(t.Context(), http.MethodGet, "https://id.zalo.me/again", nil, nil)
	if err == nil {
		t.Fatal("an endless redirect loop was followed to completion")
	}
	var transport *transportError
	if !errors.As(err, &transport) {
		t.Errorf("a redirect loop surfaced as %v, want a transportError", err)
	}
}

// TestACookieIsRefusedUnlessTheHostThatSetItOwnsTheScope is the guard on the
// worst thing a hostile answer can do here. `Domain=me` claims every `.me` site
// at once; a Zalo-shaped domain the responding host does not sit inside claims a
// sibling's. Either one, paired with a redirect, walks the member's live
// session out of the building — so the scope a response may claim is checked
// against BOTH the allowlist and the responding host.
func TestACookieIsRefusedUnlessTheHostThatSetItOwnsTheScope(t *testing.T) {
	refused := map[string]*http.Cookie{
		"a public suffix":                             {Name: "zpw_sek", Value: "stolen", Domain: "me", Path: "/"},
		"a public suffix, dot-prefixed":               {Name: "zpw_sek", Value: "stolen", Domain: ".me", Path: "/"},
		"a domain this host is not inside":            {Name: "zpw_sek", Value: "stolen", Domain: "zalo.gg", Path: "/"},
		"a domain Zalo does not own":                  {Name: "zpw_sek", Value: "stolen", Domain: "attacker.example", Path: "/"},
		"a suffix match that is not a label boundary": {Name: "zpw_sek", Value: "stolen", Domain: "alo.me", Path: "/"},
	}
	for name, hostile := range refused {
		t.Run(name, func(t *testing.T) {
			j := testJar()
			j.SetCookies(mustParse(t, "https://chat.zalo.me/index.html"), []*http.Cookie{hostile})

			if got := j.export(); len(got) != 0 {
				t.Fatalf("the jar stored a cookie scoped to %q: %+v", hostile.Domain, got)
			}
		})
	}

	// And the legitimate widening still works: chat.zalo.me may set .zalo.me,
	// which is how the real session spans the hosts it spans.
	j := testJar()
	j.SetCookies(mustParse(t, "https://chat.zalo.me/index.html"), []*http.Cookie{
		{Name: "zpsid", Value: "live", Domain: ".zalo.me", Path: "/"},
	})
	if got := cookieValues(t, j, "https://id.zalo.me/account", "zpsid"); len(got) != 1 {
		t.Fatalf("a legitimate parent-domain cookie was refused: %q", got)
	}
}

// TestACookieSetByAHostZaloDoesNotOwnIsNotStored covers the same invariant from
// the other side: a response from off-allowlist stores nothing at all, so a
// redirect that somehow reached one cannot leave a credential behind.
func TestACookieSetByAHostZaloDoesNotOwnIsNotStored(t *testing.T) {
	j := testJar()
	j.SetCookies(mustParse(t, "https://collect.attacker.example/x"), []*http.Cookie{
		{Name: "zpw_sek", Value: "planted", Path: "/"},
	})

	if got := j.export(); len(got) != 0 {
		t.Fatalf("the jar stored a cookie from a host Zalo does not own: %+v", got)
	}
}

// TestARedirectOffZalosDomainsIsRefused: every request this client makes
// carries the member's session, so the destination of a hop the RESPONSE chose
// is a place that session would be handed to.
func TestARedirectOffZalosDomainsIsRefused(t *testing.T) {
	// Guarded for the reason given at TestARedirectChainCarriesTheRefererZaloExpects.
	var (
		mu      sync.Mutex
		reached []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reached = append(reached, r.Host)
		mu.Unlock()
		if r.URL.Path == "/account/checksession" {
			w.Header().Set("Location", "https://collect.attacker.example/steal")
			w.WriteHeader(http.StatusFound)
			return
		}
		if _, err := w.Write([]byte(`{"error_code":0}`)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}))
	defer srv.Close()

	c := newClient(testOptions(t, srv, time.Second))
	_, err := c.doJSON(t.Context(), http.MethodGet, "https://id.zalo.me/account/checksession", nil, nil)
	if err == nil {
		t.Fatal("a redirect to a host Zalo does not own was followed")
	}
	if !strings.Contains(err.Error(), "attacker.example") {
		t.Errorf("error %q does not name the host that was refused", err)
	}
	mu.Lock()
	contacted := append([]string(nil), reached...)
	mu.Unlock()
	for _, host := range contacted {
		if strings.Contains(host, "attacker") {
			t.Errorf("the off-allowlist host was actually contacted: %q", host)
		}
	}
}

// TestTheJarNeverPutsASessionCookieOnAPlaintextWire: a cookie's scope includes
// the TRANSPORT. Every value in this jar is live session credential, so a
// request on a plaintext scheme would hand `zpw_sek` to anyone on the path —
// and the scheme of a URL this layer dials is not always chosen here, since the
// per-capability hosts come out of the server's own `zpw_service_map_v3`.
func TestTheJarNeverPutsASessionCookieOnAPlaintextWire(t *testing.T) {
	j := testJar()
	j.SetCookies(mustParse(t, "https://chat.zalo.me/index.html"), []*http.Cookie{
		{Name: "zpw_sek", Value: "the-session-key", Domain: ".zalo.me", Path: "/"},
	})

	for _, rawURL := range []string{"http://chat.zalo.me/api", "ws://ws4-msg.chat.zalo.me/"} {
		if got := cookieValues(t, j, rawURL, "zpw_sek"); len(got) != 0 {
			t.Errorf("the session key was sent to %s: %q", rawURL, got)
		}
	}
	// And the two encrypted schemes this layer actually speaks still get it —
	// wss because the socket handshake is hand-built and asks the jar directly,
	// so a scheme check that missed it would silently break the message drain.
	for _, rawURL := range []string{"https://chat.zalo.me/api", "wss://ws4-msg.chat.zalo.me/"} {
		if got := cookieValues(t, j, rawURL, "zpw_sek"); len(got) != 1 {
			t.Errorf("the session key did not reach %s: %q", rawURL, got)
		}
	}
}

// TestARedirectOntoPlaintextIsRefused covers the downgrade a hostile or merely
// misconfigured answer can propose: the destination is a Zalo host, so the
// allowlist admits it, and only the scheme says no. The jar would withhold the
// cookies, which makes silently proceeding the other bad outcome — a request
// stripped of its session reads as an expired login.
func TestARedirectOntoPlaintextIsRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The downgraded hop answers perfectly well, which is the point: only
		// the scheme is wrong, so nothing but the guard can stop the request.
		if r.URL.Path == "/index.html" {
			if _, err := w.Write([]byte(`{"error_code":0}`)); err != nil {
				t.Errorf("write response: %v", err)
			}
			return
		}
		w.Header().Set("Location", "http://chat.zalo.me/index.html")
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()

	c := newClient(testOptions(t, srv, time.Second))
	_, err := c.doJSON(t.Context(), http.MethodGet, "https://id.zalo.me/account/checksession", nil, nil)
	if err == nil {
		t.Fatal("a redirect that downgraded the scheme to http was followed")
	}
	if !strings.Contains(err.Error(), "http") {
		t.Errorf("error %q does not say the scheme was what was refused", err)
	}
}

func TestOnlyZalosOwnDomainsAreRecognised(t *testing.T) {
	inside := []string{"zalo.me", "chat.zalo.me", "tt-chat3-wpa.chat.zalo.me", "id.zalo.me",
		"zaloapp.com", "wpa.zaloapp.com", "zalo.gg", "zalo.cx", "CHAT.ZALO.ME"}
	outside := []string{"me", "com", "evilzalo.me", "zalo.me.attacker.example", "notzalo.me",
		"zaloapp.com.evil.co", "attacker.example", ""}

	for _, host := range inside {
		if !isZaloHost(host) {
			t.Errorf("%q is one of Zalo's own and was not recognised", host)
		}
	}
	for _, host := range outside {
		if isZaloHost(host) {
			t.Errorf("%q is not Zalo's and was recognised as theirs", host)
		}
	}
}

// TestAFailedRequestNeverReportsTheQueryString is the guard on this layer's
// quietest leak. `imei` is the device identity, and `zcid`/`zcid_ext` derive the
// ephemeral login key through a constant Zalo publishes in its own bundle — so
// a logged URL is enough to decrypt the `params` blob beside it. An error is the
// one value on this path designed to be written down.
func TestAFailedRequestNeverReportsTheQueryString(t *testing.T) {
	secrets := map[string]string{
		"imei":     "6f4a9a4c-1a2b-4c3d-8e9f-000000000001-7b1a0e4d0f1c2b3a4d5e6f7a8b9c0d1e",
		"zcid":     "23DB26D57EB064F3422E5FE7AF697DFF",
		"zcid_ext": "a1b2c3d4e5f6",
		"params":   "3EHsMU9D7G81gX8vUfsKm1zdMLQUEs",
		"signkey":  "2f46dc4a084eac5ee45d40bafff04b23",
	}
	rawURL, err := makeURL("https://wpa.chat.zalo.me/api/login/getLoginInfo", secrets, true)
	if err != nil {
		t.Fatalf("make url: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	c := newClient(testOptions(t, srv, time.Second))
	_, err = c.doJSON(t.Context(), http.MethodGet, rawURL, nil, nil)
	if err == nil {
		t.Fatal("a 502 answer produced no error")
	}

	reported := fmt.Sprintf("%v %+v", err, err)
	for name, value := range secrets {
		if strings.Contains(reported, value) {
			t.Errorf("the error reports the %s value: %s", name, reported)
		}
	}
	// It still has to be useful: the endpoint that failed must be nameable.
	if !strings.Contains(reported, "wpa.chat.zalo.me/api/login/getLoginInfo") {
		t.Errorf("the error no longer says which endpoint failed: %s", reported)
	}
}

// TestNoErrorInThisFileCanCarryTheRawURL is a fitness function, not a case.
//
// The query-string leak has now been introduced twice, both times the same way:
// somebody added an error path and formatted the variable that was in scope.
// A test per path only ever proves the paths that exist today, so this one
// derives the obligation from the source instead — in zalohttp.go, no error may
// be built from `rawURL`; the redacted `safe` is the only URL an error sees.
//
// Reading the file is the point. A behavioural test cannot see a path nobody
// has written yet, and this one fails the moment somebody writes it.
func TestNoErrorInThisFileCanCarryTheRawURL(t *testing.T) {
	const file = "zalohttp.go"

	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}

	mentionsRawURL := func(node ast.Node) bool {
		var found bool
		ast.Inspect(node, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && id.Name == "rawURL" {
				found = true
			}
			return !found
		})
		return found
	}

	ast.Inspect(parsed, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CallExpr:
			// fmt.Errorf(…) — the wrapped-error spelling.
			sel, ok := node.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Errorf" {
				return true
			}
			for _, arg := range node.Args {
				if mentionsRawURL(arg) {
					t.Errorf("%s: this error is built from rawURL, which carries imei/zcid/zcid_ext/params/signkey as query parameters — use the redacted `safe`", fset.Position(arg.Pos()))
				}
			}
		case *ast.CompositeLit:
			// &transportError{…} — the structured spelling.
			if id, ok := node.Type.(*ast.Ident); !ok || id.Name != "transportError" {
				return true
			}
			for _, elt := range node.Elts {
				if mentionsRawURL(elt) {
					t.Errorf("%s: this transportError is built from rawURL — use the redacted `safe`", fset.Position(elt.Pos()))
				}
			}
		}
		return true
	})
}

// TestTheRequestBuildPathIsRedactedToo is the case the fitness function above
// was written for: a URL so malformed that no request can be built from it, on
// the one error path the 502 test never reached.
func TestTheRequestBuildPathIsRedactedToo(t *testing.T) {
	const secret = "6f4a9a4c-1a2b-4c3d-8e9f-000000000001"

	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	c := newClient(testOptions(t, srv, time.Second))
	// A control character in the method is refused by http.NewRequest, which
	// is the branch that formats the URL before anything is sent.
	_, err := c.doJSON(t.Context(), "BAD METHOD", "https://wpa.chat.zalo.me/api/login/getLoginInfo?imei="+secret, nil, nil)
	if err == nil {
		t.Fatal("an unbuildable request produced no error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("the request-build error reports the imei: %v", err)
	}
	if !strings.Contains(err.Error(), "wpa.chat.zalo.me") {
		t.Errorf("the request-build error no longer says which endpoint failed: %v", err)
	}
}

// refuseToDial is a transport that never reaches anybody, which is the commonest
// failure on this path: a timeout, a DNS miss, a refused connection, a TLS handshake
// Zalo did not complete, or the per-member budget expiring mid-handshake.
type refuseToDial struct{ cause error }

func (r refuseToDial) RoundTrip(*http.Request) (*http.Response, error) { return nil, r.cause }

// TestAFailedRoundTripReportsNoQueryParameters is the behavioural half of the guard
// the fitness function above states: http.Client.Do answers a *url.Error whose own
// Error() prints the WHOLE request URL, query included — the stdlib redacts userinfo
// and nothing else — so wrapping it verbatim writes imei, zcid, zcid_ext, params and
// signkey into the job runner's log and the member's delivery record, which is the
// leak safeURL exists to prevent.
func TestAFailedRoundTripReportsNoQueryParameters(t *testing.T) {
	const (
		imei    = "6f4a9a4c-1a2b-4c3d-8e9f-000000000002"
		zcid    = "8ff1a0b3c2d4e5f6"
		signkey = "d41d8cd98f00b204e9800998ecf8427e"
	)
	c := newClient(zaloOptions{
		Transport: refuseToDial{cause: errors.New("dial tcp 203.0.113.7:443: connect: connection refused")},
		Now:       newFakeClock(time.Second).now,
	})

	_, err := c.doJSON(t.Context(), http.MethodGet,
		"https://wpa.chat.zalo.me/api/login/getLoginInfo?imei="+imei+"&zcid="+zcid+
			"&zcid_ext=9911&params=ZW5jcnlwdGVkLWJsb2I&signkey="+signkey, nil, nil)
	if err == nil {
		t.Fatal("a round trip that never reached anybody produced no error")
	}
	for _, secret := range []string{imei, zcid, signkey, "9911", "ZW5jcnlwdGVkLWJsb2I",
		"imei=", "zcid=", "zcid_ext=", "params=", "signkey="} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("the transport failure reports %q: %v", secret, err)
		}
	}
	if !strings.Contains(err.Error(), "wpa.chat.zalo.me/api/login/getLoginInfo") {
		t.Errorf("the transport failure no longer says which endpoint failed: %v", err)
	}
}

// TestEveryTransportErrorIsBuiltThroughTheRedactingHelper derives the obligation from
// the source rather than maintaining a list of wrap sites: the leak above arrives
// INSIDE the wrapped error, where neither the 502 test nor the rawURL fitness function
// can see it, and the only durable answer is that one function does the redacting.
//
// It reads the WHOLE unit rather than one file, because the next wrap site will be
// written wherever the next endpoint is.
func TestEveryTransportErrorIsBuiltThroughTheRedactingHelper(t *testing.T) {
	const helper = "unreachable"

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the unit: %v", err)
	}
	fset := token.NewFileSet()
	var read int
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		read++
		forbidTransportErrorLiterals(t, fset, parsed, helper)
	}
	if read == 0 {
		t.Fatal("this check read no source at all, so its silence means nothing")
	}
}

// forbidTransportErrorLiterals reports every transportError built outside the helper
// in one file.
func forbidTransportErrorLiterals(t *testing.T, fset *token.FileSet, parsed *ast.File, helper string) {
	t.Helper()
	for _, decl := range parsed.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name == helper {
			continue
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			if id, isNamed := lit.Type.(*ast.Ident); isNamed && id.Name == "transportError" {
				t.Errorf("%s: %s builds a transportError itself; the wrapped error may be a *url.Error carrying the query, so it goes through %s()",
					fset.Position(lit.Pos()), fn.Name.Name, helper)
			}
			return true
		})
	}
}
