// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package webread

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestStripTagsSurvivesUnicodeCaseFolding(t *testing.T) {
	// U+212A (KELVIN SIGN) lowercases to a 1-byte "k": an index into a
	// lowered copy of the document would drift off the source bytes and
	// slice out of range. The stripper must work on the original bytes.
	kelvin := strings.Repeat("\u212a", 3)
	got := StripTags(kelvin + "<p>hello</p><script>evil()</script> world")
	if !strings.HasSuffix(got, "hello world") {
		t.Fatalf("StripTags = %q", got)
	}
	if StripTags("<SCRIPT>x</SCRIPT>visible<STYLE>y</STYLE>") != "visible" {
		t.Fatal("case-insensitive script/style stripping broke")
	}
}

// The policy reading is REP (RFC 9309): longest match wins, Allow beats
// Disallow at equal length, the group naming this bot beats *, and an empty
// Disallow means allow-all.
func TestRobotsPolicyReading(t *testing.T) {
	cases := []struct {
		name    string
		robots  string
		path    string
		allowed bool
	}{
		{"no policy at all", "", "/anything", true},
		{"wildcard disallow all", "User-agent: *\nDisallow: /", "/impressum", false},
		{"disallow one tree", "User-agent: *\nDisallow: /private/", "/impressum", true},
		{"disallow that tree", "User-agent: *\nDisallow: /private/", "/private/x", false},
		{"longest match wins", "User-agent: *\nDisallow: /a/\nAllow: /a/public/", "/a/public/page", true},
		{"allow wins at equal length", "User-agent: *\nAllow: /a/\nDisallow: /a/", "/a/x", true},
		{"our group beats wildcard", "User-agent: *\nDisallow: /\n\nUser-agent: margince-siteread\nAllow: /", "/impressum", true},
		{"our group can also be stricter", "User-agent: *\nAllow: /\n\nUser-agent: margince-siteread\nDisallow: /", "/impressum", false},
		{"empty disallow is allow-all", "User-agent: *\nDisallow:", "/x", true},
		{"comments and case ignored", "# hi\nUSER-AGENT: *\nDISALLOW: /x # trailing", "/x/y", false},
		{"stacked agent lines share one group", "User-agent: otherbot\nUser-agent: *\nDisallow: /x", "/x", false},
		{"rules before any agent line bind nobody", "Disallow: /\nUser-agent: *\nAllow: /", "/x", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			policy := parseRobots(tc.robots)
			if got := policy.allows(tc.path); got != tc.allowed {
				t.Fatalf("allows(%q) = %v, want %v\nrobots:\n%s", tc.path, got, tc.allowed, tc.robots)
			}
		})
	}
}

// testFetcher builds a Fetcher over an unguarded client: httptest servers live
// on loopback, which the production SSRF guard rightly refuses — the guard has
// its own coverage in platform/netguard, this suite covers the robots gate.
func testFetcher() *Fetcher {
	return &Fetcher{
		client: &http.Client{Timeout: time.Second},
		robots: map[string]robotsEntry{},
		now:    time.Now,
	}
}

func TestFetchHonorsTheSitesRobotsAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			//craft:ignore swallowed-errors httptest handler write; a failed write fails the test through the assertion below
			_, _ = w.Write([]byte("User-agent: *\nDisallow: /private/\n"))
		case "/impressum":
			//craft:ignore swallowed-errors httptest handler write; a failed write fails the test through the assertion below
			_, _ = w.Write([]byte("<html><body>Acme GmbH, HRB 12345</body></html>"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	f := testFetcher()

	// An allowed path fetches and strips.
	text, err := f.Fetch(context.Background(), srv.URL+"/impressum")
	if err != nil {
		t.Fatalf("allowed fetch: %v", err)
	}
	if text != "Acme GmbH, HRB 12345" {
		t.Fatalf("stripped text = %q", text)
	}

	// A disallowed path is refused as the site's answer, not fetched anyway.
	if _, err := f.Fetch(context.Background(), srv.URL+"/private/report"); !errors.Is(err, ErrRobotsDisallowed) {
		t.Fatalf("disallowed fetch → %v, want ErrRobotsDisallowed", err)
	}
}

func TestFetchWithoutARobotsPolicyProceeds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r) // 4xx: the site declares no policy
			return
		}
		//craft:ignore swallowed-errors httptest handler write; a failed write fails the test through the assertion below
		_, _ = w.Write([]byte("hello"))
	}))
	defer srv.Close()

	text, err := testFetcher().Fetch(context.Background(), srv.URL+"/page")
	if err != nil || text != "hello" {
		t.Fatalf("fetch under a 404 robots = %q, %v — a missing policy is allow-all", text, err)
	}
}

func TestFetchRefusesWhenRobotsCannotAnswer(t *testing.T) {
	// A 5xx robots is NOT "no policy": the site could not say what it
	// permits, and that must never resolve in the bot's own favor.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		//craft:ignore swallowed-errors httptest handler write; unreachable when the test passes
		_, _ = w.Write([]byte("hello"))
	}))
	defer srv.Close()

	if _, err := testFetcher().Fetch(context.Background(), srv.URL+"/page"); err == nil {
		t.Fatal("fetch proceeded although robots.txt answered 500")
	}
}

func TestRobotsPolicyIsCachedPerHost(t *testing.T) {
	robotsHits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			robotsHits++
			//craft:ignore swallowed-errors httptest handler write; a failed write fails the test through the assertion below
			_, _ = w.Write([]byte("User-agent: *\nAllow: /\n"))
			return
		}
		//craft:ignore swallowed-errors httptest handler write; a failed write fails the test through the assertion below
		_, _ = w.Write([]byte("page"))
	}))
	defer srv.Close()
	f := testFetcher()

	for range 3 {
		if _, err := f.Fetch(context.Background(), srv.URL+"/a"); err != nil {
			t.Fatal(err)
		}
	}
	if robotsHits != 1 {
		t.Fatalf("robots.txt fetched %d times for one host within the TTL, want 1", robotsHits)
	}
}

func TestProductionFetcherRefusesPrivateAddresses(t *testing.T) {
	// The REAL constructor, guard included: an httptest server lives on
	// loopback, which is exactly the address class a tenant-supplied URL must
	// never reach. The refusal fires on the very first dial — the robots
	// lookup — so nothing is ever fetched from the private address at all.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the SSRF guard let a request through to a loopback address")
	}))
	defer srv.Close()

	_, err := New().Fetch(context.Background(), srv.URL+"/page")
	if err == nil || !strings.Contains(err.Error(), "refusing non-public address") {
		t.Fatalf("fetch of a loopback URL → %v, want the SSRF refusal", err)
	}
}

func TestFetchRefusesAnUnfetchableURL(t *testing.T) {
	if _, err := testFetcher().Fetch(context.Background(), "not-a-url"); err == nil {
		t.Fatal("fetch accepted a URL with no host")
	}
}
