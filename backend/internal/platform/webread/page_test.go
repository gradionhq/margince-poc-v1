// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package webread

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestLinkExtraction(t *testing.T) {
	base, err := url.Parse("https://acme.example/blog/post")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		html string
		want []string
	}{
		{
			"relative hrefs resolve against the page URL",
			`<a href="../about">a</a><a href="/team">b</a><a href="pricing">c</a>`,
			[]string{"https://acme.example/about", "https://acme.example/team", "https://acme.example/blog/pricing"},
		},
		{
			"fragments are dropped, fragment-only links collapse to the page",
			`<a href="/x#top">a</a><a href="#main">b</a>`,
			[]string{"https://acme.example/x", "https://acme.example/blog/post"},
		},
		{
			"duplicates collapse to the first occurrence",
			`<a href="/a">1</a><a href="/b">2</a><a href="/a">3</a>`,
			[]string{"https://acme.example/a", "https://acme.example/b"},
		},
		{
			"non-http schemes are dropped",
			`<a href="mailto:x@acme.example">m</a><a href="javascript:void(0)">j</a><a href="tel:+491234">t</a><a href="/kept">k</a>`,
			[]string{"https://acme.example/kept"},
		},
		{
			"an href inside a script string is not a link",
			`<script>document.write('<a href="/hidden">x</a>')</script><a href="/real">r</a>`,
			[]string{"https://acme.example/real"},
		},
		{
			"absolute and protocol-relative hrefs pass through resolved",
			`<a href="https://other.example/p">o</a><a href="//cdn.example/lib">c</a>`,
			[]string{"https://other.example/p", "https://cdn.example/lib"},
		},
		{
			"an anchor without an href yields nothing",
			`<a name="top">t</a>`,
			nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractLinks(tc.html, base); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("extractLinks = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFetchPageKeepsTheTextContractAndHarvestsLinks(t *testing.T) {
	pageHTML := `<html><body><nav><a href="/impressum">Impressum</a></nav>Acme GmbH builds robots.</body></html>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		//craft:ignore swallowed-errors httptest handler write; a failed write fails the test through the assertions below
		_, _ = w.Write([]byte(pageHTML))
	}))
	defer srv.Close()
	f := testFetcher()

	page, err := f.FetchPage(context.Background(), srv.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	// The text is byte-identical to what single-page Fetch callers see —
	// evidence snippets are matched against this exact reduction.
	if page.Text != StripTags(pageHTML) {
		t.Fatalf("Page.Text = %q diverged from StripTags", page.Text)
	}
	if text, err := f.Fetch(context.Background(), srv.URL+"/"); err != nil || text != page.Text {
		t.Fatalf("Fetch = %q, %v — must delegate to the same reduction", text, err)
	}
	if want := []string{srv.URL + "/impressum"}; !reflect.DeepEqual(page.Links, want) {
		t.Fatalf("Page.Links = %v, want %v", page.Links, want)
	}
	if page.Bytes != len(pageHTML) {
		t.Fatalf("Page.Bytes = %d, want the raw size %d", page.Bytes, len(pageHTML))
	}
}

func TestSitemapLocParsing(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string
	}{
		{
			"urlset yields page URLs",
			`<?xml version="1.0"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
				<url><loc>https://acme.example/</loc><lastmod>2026-01-01</lastmod></url>
				<url><loc> https://acme.example/team </loc></url>
			</urlset>`,
			[]string{"https://acme.example/", "https://acme.example/team"},
		},
		{
			// Non-recursion: an index's <loc>s are child sitemap URLs and come
			// back exactly as written — the caller ignores them, we never
			// chase nested indexes.
			"sitemapindex yields the child sitemap URLs, unrecursed",
			`<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
				<sitemap><loc>https://acme.example/sitemap-pages.xml</loc></sitemap>
				<sitemap><loc>https://acme.example/sitemap-blog.xml</loc></sitemap>
			</sitemapindex>`,
			[]string{"https://acme.example/sitemap-pages.xml", "https://acme.example/sitemap-blog.xml"},
		},
		{
			"an empty urlset yields nothing",
			`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"></urlset>`,
			nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseSitemapLocs(tc.body)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("locs = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSitemapAbsenceIsNormal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r) // no robots.txt, no sitemap.xml
	}))
	defer srv.Close()

	locs, err := testFetcher().FetchSitemap(context.Background(), srv.URL)
	if err != nil || locs != nil {
		t.Fatalf("missing sitemap = %v, %v — absence must be an empty list, not an error", locs, err)
	}
}

func TestSitemapFetchHonorsRobots(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			//craft:ignore swallowed-errors httptest handler write; a failed write fails the test through the assertion below
			_, _ = w.Write([]byte("User-agent: *\nDisallow: /sitemap.xml\n"))
			return
		}
		t.Errorf("fetched %s although robots disallows it", r.URL.Path)
	}))
	defer srv.Close()

	if _, err := testFetcher().FetchSitemap(context.Background(), srv.URL); !errors.Is(err, ErrRobotsDisallowed) {
		t.Fatalf("disallowed sitemap fetch → %v, want ErrRobotsDisallowed", err)
	}
}

func TestSameRegistrableDomain(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want bool
	}{
		{"subdomain and apex share the site", "https://blog.acme.de/post", "https://acme.de", true},
		{"www is a subdomain like any other", "https://www.acme.de", "http://acme.de/impressum", true},
		{"same name under different TLDs differs", "https://acme.de", "https://acme.com", false},
		{"co.uk is a suffix, not a shared site", "https://foo.co.uk", "https://bar.co.uk", false},
		{"a deep public suffix still finds its eTLD+1", "https://shop.foo.co.uk", "https://foo.co.uk", true},
		{"an unparseable URL is never same-site", "://broken", "https://acme.de", false},
		{"a hostless URL is never same-site", "https://acme.de", "/relative/only", false},
		{"hostname case does not matter", "https://ACME.de", "https://acme.DE", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SameRegistrableDomain(tc.a, tc.b); got != tc.want {
				t.Fatalf("SameRegistrableDomain(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// pacerClock is the sleep-free seam pair: sleeping advances the fake clock,
// so pacing is proven through the durations requested, never wall time.
type pacerClock struct {
	mu    sync.Mutex
	t     time.Time
	slept []time.Duration
}

func (c *pacerClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *pacerClock) sleep(_ context.Context, d time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.slept = append(c.slept, d)
	c.t = c.t.Add(d)
	return nil
}

func testPacer(clock *pacerClock) *Pacer {
	p := NewPacer()
	p.now = clock.now
	p.sleep = clock.sleep
	return p
}

func TestPacerSpacesRequestStarts(t *testing.T) {
	clock := &pacerClock{t: time.Unix(1000, 0)}
	p := testPacer(clock)

	// The first start owes no wait; the second must sleep out the full
	// interval because no fake time has passed between them.
	for i := range 2 {
		if err := p.Wait(context.Background()); err != nil {
			t.Fatalf("Wait %d: %v", i, err)
		}
		p.Done()
	}
	if want := []time.Duration{pacerMinInterval}; !reflect.DeepEqual(clock.slept, want) {
		t.Fatalf("sleeps = %v, want %v — exactly the second start pays the interval", clock.slept, want)
	}
}

func TestPacerOverBudgetConcurrentRequestWaits(t *testing.T) {
	clock := &pacerClock{t: time.Unix(1000, 0)}
	p := testPacer(clock)
	for i := range pacerMaxConcurrent {
		if err := p.Wait(context.Background()); err != nil {
			t.Fatalf("Wait %d: %v — starts within the slot budget must proceed", i, err)
		}
	}

	// With every slot held, the next Wait must block — proven by the fact
	// that cancellation, not completion, is what it returns.
	ctx, cancel := context.WithCancel(context.Background())
	blocked := make(chan error, 1)
	go func() { blocked <- p.Wait(ctx) }()
	cancel()
	if err := <-blocked; !errors.Is(err, context.Canceled) {
		t.Fatalf("over-budget concurrent Wait = %v, want context.Canceled", err)
	}

	// Once a slot is released, a fresh Wait proceeds.
	p.Done()
	if err := p.Wait(context.Background()); err != nil {
		t.Fatalf("Wait after Done: %v", err)
	}
}

func TestPacerCancelDuringSleepReleasesTheSlot(t *testing.T) {
	clock := &pacerClock{t: time.Unix(1000, 0)}
	p := testPacer(clock)
	if err := p.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}

	// The second Wait holds a slot while sleeping out the interval; the sleep
	// is cancelled. The slot MUST come back, or the pacer leaks capacity.
	p.sleep = func(context.Context, time.Duration) error { return context.Canceled }
	if err := p.Wait(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Wait = %v, want context.Canceled", err)
	}

	p.sleep = clock.sleep
	if err := p.Wait(context.Background()); err != nil {
		t.Fatalf("Wait after a cancelled Wait: %v — the cancelled Wait leaked its slot", err)
	}
}
