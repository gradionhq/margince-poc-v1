// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The deep-read crawler's contract: bounded by the R2 caps, same-site only,
// discovery deterministic — nothing a hostile page writes can widen the crawl
// beyond the seed's own site.

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/webread"
)

// fakeSite is an in-memory site behind the siteFetcher seam. It records every
// URL the crawler asked for, so tests can assert what was NEVER fetched.
type fakeSite struct {
	pages   map[string]fakeSitePage
	sitemap []string
	// mu guards fetched/onFetch: the production crawler fetches waves
	// concurrently even though tests pin the wave to 1.
	mu      sync.Mutex
	fetched []string
	onFetch func(url string)
}

type fakeSitePage struct {
	text   string
	links  []string
	robots bool
}

func (s *fakeSite) FetchPage(_ context.Context, rawURL string) (webread.Page, error) {
	s.mu.Lock()
	s.fetched = append(s.fetched, rawURL)
	onFetch := s.onFetch
	s.mu.Unlock()
	if onFetch != nil {
		onFetch(rawURL)
	}
	page, ok := s.pages[rawURL]
	if !ok {
		return webread.Page{}, errors.New("fake site: no such page")
	}
	if page.robots {
		return webread.Page{}, webread.ErrRobotsDisallowed
	}
	return webread.Page{URL: rawURL, Text: page.text, Links: page.links, Bytes: len(page.text)}, nil
}

func (s *fakeSite) FetchSitemap(context.Context, string) ([]string, error) {
	return s.sitemap, nil
}

// instantPacer removes real-clock politeness from crawler tests; pacing has
// its own proof in platform/webread.
type instantPacer struct{}

func (instantPacer) Wait(context.Context) error { return nil }
func (instantPacer) Done()                      {}

func testSiteCrawler(site *fakeSite) *siteCrawler {
	crawler := newSiteCrawler(site, CrawlCaps{})
	crawler.newPacer = func() crawlPacer { return instantPacer{} }
	// Wave of one: the tests' fetch logs and scripted fixtures read in
	// strict crawl order; wave concurrency has its own test.
	crawler.fetchWave = 1
	return crawler
}

// readable pads a marker out past the minimum-rune floor while keeping every
// page's text distinct, so the duplicate-text skip never fires by accident.
func readable(marker string) string {
	return marker + " " + strings.Repeat("Substantive prose about the business. ", 4)
}

const seedURL = "https://acme.example"

// seedOnly builds a site of just the landing page. Links are given as paths
// and made absolute here — webread.FetchPage resolves hrefs before the
// crawler ever sees them, so the fake speaks the same contract.
func seedOnly(linkPaths ...string) map[string]fakeSitePage {
	links := make([]string, 0, len(linkPaths))
	for _, path := range linkPaths {
		if strings.HasPrefix(path, "https://") {
			links = append(links, path)
			continue
		}
		links = append(links, seedURL+path)
	}
	return map[string]fakeSitePage{
		seedURL: {text: readable("Welcome to Acme."), links: links},
	}
}

func TestCrawlCapsZeroValueTakesTheDefaultsAndExplicitCapsHold(t *testing.T) {
	defaulted := newSiteCrawler(&fakeSite{}, CrawlCaps{})
	if defaulted.maxPages != defaultCrawlMaxPages || defaulted.maxBytes != defaultCrawlMaxBytes || defaulted.wall != defaultCrawlWall {
		t.Fatalf("zero caps gave %d pages / %d bytes / %s, want the defaults %d / %d / %s",
			defaulted.maxPages, defaulted.maxBytes, defaulted.wall,
			defaultCrawlMaxPages, defaultCrawlMaxBytes, defaultCrawlWall)
	}
	explicit := newSiteCrawler(&fakeSite{}, CrawlCaps{MaxPages: 3, MaxBytes: 1 << 10, Wall: time.Second})
	if explicit.maxPages != 3 || explicit.maxBytes != 1<<10 || explicit.wall != time.Second {
		t.Fatalf("explicit caps not honored: %d pages / %d bytes / %s", explicit.maxPages, explicit.maxBytes, explicit.wall)
	}
}

func TestCrawlWithoutASeedPageIsAFailureNotAPartialRead(t *testing.T) {
	site := &fakeSite{pages: map[string]fakeSitePage{}}
	if _, err := testSiteCrawler(site).Crawl(context.Background(), seedURL); err == nil {
		t.Fatal("a crawl whose seed page failed returned a result")
	}
}

func TestCrawlStopsAtThePageCapAndRecordsWhatWasCut(t *testing.T) {
	const maxPages = 12
	site := &fakeSite{pages: seedOnly()}
	for i := range 40 {
		pageURL := fmt.Sprintf("%s/page-%02d", seedURL, i)
		site.sitemap = append(site.sitemap, pageURL)
		site.pages[pageURL] = fakeSitePage{text: readable(pageURL)}
	}

	crawler := testSiteCrawler(site)
	crawler.maxPages = maxPages
	crawl, err := crawler.Crawl(context.Background(), seedURL)
	if err != nil {
		t.Fatal(err)
	}
	if len(crawl.Pages) != maxPages {
		t.Fatalf("fetched %d pages, want the cap %d", len(crawl.Pages), maxPages)
	}
	if crawl.Stopped == nil || *crawl.Stopped != crmcontracts.SiteReadReportStoppedReasonPageCap {
		t.Fatalf("Stopped = %v, want page_cap", crawl.Stopped)
	}
	var capSkips int
	for _, skip := range crawl.Skipped {
		if skip.Reason == crmcontracts.SiteReadSkipReasonPageCap {
			capSkips++
		}
	}
	// 40 sitemap URLs minus the 11 fetched leaves far more than the report
	// bound; the record must show the cut without ballooning.
	if capSkips != crawlSkipReportCap {
		t.Fatalf("recorded %d page_cap skips, want the report bound %d", capSkips, crawlSkipReportCap)
	}
}

func TestCrawlStopsAtTheByteCap(t *testing.T) {
	site := &fakeSite{pages: seedOnly()}
	pageURL := seedURL + "/big"
	site.sitemap = []string{pageURL, seedURL + "/never-reached"}
	site.pages[pageURL] = fakeSitePage{text: readable("big page")}

	crawler := testSiteCrawler(site)
	// The seed plus one page overflow this budget; probes 404 and add nothing.
	crawler.maxBytes = len(site.pages[seedURL].text) + len(site.pages[pageURL].text)

	crawl, err := crawler.Crawl(context.Background(), seedURL)
	if err != nil {
		t.Fatal(err)
	}
	if crawl.Stopped == nil || *crawl.Stopped != crmcontracts.SiteReadReportStoppedReasonByteCap {
		t.Fatalf("Stopped = %v, want byte_cap", crawl.Stopped)
	}
	var found bool
	for _, skip := range crawl.Skipped {
		if skip.URL == seedURL+"/never-reached" && skip.Reason == crmcontracts.SiteReadSkipReasonByteCap {
			found = true
		}
	}
	if !found {
		t.Fatalf("the candidate the byte cap cut is not in Skipped: %v", crawl.Skipped)
	}
}

func TestCrawlRecordsARobotsRefusalAsASkip(t *testing.T) {
	site := &fakeSite{pages: seedOnly()}
	site.pages[seedURL+"/impressum"] = fakeSitePage{robots: true}

	crawl, err := testSiteCrawler(site).Crawl(context.Background(), seedURL)
	if err != nil {
		t.Fatal(err)
	}
	want := crawlSkip{URL: seedURL + "/impressum", Reason: crmcontracts.SiteReadSkipReasonRobots}
	var found bool
	for _, skip := range crawl.Skipped {
		if skip == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("robots refusal not recorded; skipped = %v", crawl.Skipped)
	}
}

func TestCrawlNeverFollowsAnOffDomainLink(t *testing.T) {
	// The security property: link discovery reads content a stranger wrote.
	// A hostile page pointing the crawler at another domain — an internal
	// service, a victim site, a tarpit — must be recorded and NEVER fetched.
	hostileTarget := "https://evil.example/exfil"
	site := &fakeSite{pages: seedOnly(hostileTarget, "https://sub.acme.example/team")}
	site.pages["https://sub.acme.example/team"] = fakeSitePage{text: readable("Our team")}

	crawl, err := testSiteCrawler(site).Crawl(context.Background(), seedURL)
	if err != nil {
		t.Fatal(err)
	}
	for _, fetchedURL := range site.fetched {
		if fetchedURL == hostileTarget {
			t.Fatal("the crawler fetched an off-domain URL a page linked to")
		}
	}
	var offDomainRecorded, subdomainFetched bool
	for _, skip := range crawl.Skipped {
		if skip.URL == hostileTarget && skip.Reason == crmcontracts.SiteReadSkipReasonOffDomain {
			offDomainRecorded = true
		}
	}
	for _, page := range crawl.Pages {
		if page.URL == "https://sub.acme.example/team" {
			subdomainFetched = true
		}
	}
	if !offDomainRecorded {
		t.Fatalf("off-domain link not recorded as a skip: %v", crawl.Skipped)
	}
	// Same registrable domain is the line: a subdomain is still the site.
	if !subdomainFetched {
		t.Fatal("a same-site subdomain link was not followed")
	}
}

func TestCrawlSkipsADuplicateTextPageSilently(t *testing.T) {
	// An SPA catch-all answers every path with the landing page. That page is
	// neither new evidence nor honest degradation, so it must appear in
	// neither Pages nor Skipped.
	site := &fakeSite{pages: seedOnly()}
	site.pages[seedURL+"/about"] = fakeSitePage{text: site.pages[seedURL].text}

	crawl, err := testSiteCrawler(site).Crawl(context.Background(), seedURL)
	if err != nil {
		t.Fatal(err)
	}
	for _, page := range crawl.Pages {
		if page.URL == seedURL+"/about" {
			t.Fatal("the duplicate-text page was kept as a page")
		}
	}
	for _, skip := range crawl.Skipped {
		if skip.URL == seedURL+"/about" {
			t.Fatalf("the duplicate-text page was reported as a skip (%s)", skip.Reason)
		}
	}
}

func TestCrawlClassifiesPageKinds(t *testing.T) {
	site := &fakeSite{pages: seedOnly("/karriere")}
	for path, text := range map[string]string{
		"/impressum": "Acme GmbH, HRB 12345",
		"/team":      "The people",
		"/kontakt":   "Reach us",
		"/services":  "What we do",
		"/karriere":  "Open roles", // discovered link, no kind keyword → other
	} {
		site.pages[seedURL+path] = fakeSitePage{text: readable(text)}
	}
	// Both Impressum spellings exist; the probe order must take exactly one.
	site.pages[seedURL+"/imprint"] = fakeSitePage{text: readable("The same notice again, alternate URL")}

	crawl, err := testSiteCrawler(site).Crawl(context.Background(), seedURL)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]crmcontracts.SiteReadPageKind{}
	for _, page := range crawl.Pages {
		kinds[page.URL] = page.Kind
	}
	want := map[string]crmcontracts.SiteReadPageKind{
		seedURL:                crmcontracts.SiteReadPageKindHome,
		seedURL + "/impressum": crmcontracts.SiteReadPageKindImpressum,
		seedURL + "/team":      crmcontracts.SiteReadPageKindTeam,
		seedURL + "/kontakt":   crmcontracts.SiteReadPageKindContact,
		seedURL + "/services":  crmcontracts.SiteReadPageKindServices,
		seedURL + "/karriere":  crmcontracts.SiteReadPageKindOther,
	}
	if !reflect.DeepEqual(kinds, want) {
		t.Fatalf("kinds = %v, want %v", kinds, want)
	}
	for _, fetchedURL := range site.fetched {
		if fetchedURL == seedURL+"/imprint" {
			t.Fatal("a second Impressum probe was fetched although the kind was already satisfied")
		}
	}
}

func TestCrawlOrderIsDeterministicAcrossRuns(t *testing.T) {
	build := func() *fakeSite {
		site := &fakeSite{pages: seedOnly("/blog", "/pricing")}
		site.sitemap = []string{seedURL + "/cases"}
		for _, path := range []string{"/about", "/team", "/blog", "/pricing", "/cases"} {
			site.pages[seedURL+path] = fakeSitePage{text: readable(path)}
		}
		return site
	}

	first, err := testSiteCrawler(build()).Crawl(context.Background(), seedURL)
	if err != nil {
		t.Fatal(err)
	}
	second, err := testSiteCrawler(build()).Crawl(context.Background(), seedURL)
	if err != nil {
		t.Fatal(err)
	}
	// Fetch timing is wall-clock observability, the one field two
	// identical crawls legitimately disagree on.
	for i := range first.Pages {
		first.Pages[i].FetchDur = 0
	}
	for i := range second.Pages {
		second.Pages[i].FetchDur = 0
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("two crawls of the same site diverged:\n%v\n%v", first, second)
	}
	// And the order itself is the documented one: probes first, then
	// discovered URLs by kind priority (insertion order breaking ties),
	// boilerplate archives (/blog) last.
	var order []string
	for _, page := range first.Pages {
		order = append(order, page.URL)
	}
	want := []string{seedURL, seedURL + "/about", seedURL + "/team", seedURL + "/cases", seedURL + "/pricing", seedURL + "/blog"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("page order = %v, want %v", order, want)
	}
}

func TestCrawlSpendsAScarcePageBudgetOnFactPagesBeforeBlogLinks(t *testing.T) {
	site := &fakeSite{pages: seedOnly()}
	// Thirty blog posts arrive in the sitemap BEFORE the late-discovered
	// legal and about pages; under a small cap the kind ranking must
	// still fetch the fact pages.
	for i := range 30 {
		postURL := fmt.Sprintf("%s/blog/post-%02d", seedURL, i)
		site.sitemap = append(site.sitemap, postURL)
		site.pages[postURL] = fakeSitePage{text: readable(postURL)}
	}
	site.sitemap = append(site.sitemap, seedURL+"/de/impressum-seite", seedURL+"/ueber-uns-firma")
	site.pages[seedURL+"/de/impressum-seite"] = fakeSitePage{text: readable("Impressum. Acme GmbH.")}
	site.pages[seedURL+"/ueber-uns-firma"] = fakeSitePage{text: readable("Über uns.")}

	crawler := testSiteCrawler(site)
	crawler.maxPages = 3 // the seed plus two more
	crawl, err := crawler.Crawl(context.Background(), seedURL)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, page := range crawl.Pages {
		got = append(got, page.URL)
	}
	want := []string{seedURL, seedURL + "/de/impressum-seite", seedURL + "/ueber-uns-firma"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the budget went to %v, want the fact pages %v", got, want)
	}
}

func TestCrawlReadsOneLanguagePerDocumentAndBoundsTheLegalCensus(t *testing.T) {
	site := &fakeSite{pages: seedOnly()}
	// The same three documents mounted under four locales, plus one page
	// that exists ONLY under a locale prefix — that one must still be
	// read. Legal pages relax the collapse, because a group's per-locale
	// imprints can name different entities and the conflict guard can
	// only count what it reads — but the relaxation is BOUNDED
	// (maxLegalLocalePages): past it a translation is just a translation,
	// and on a six-language site the unbounded rule spent the page budget
	// on restatements of one legal notice.
	for _, path := range []string{"/about", "/imprint", "/pricing"} {
		site.sitemap = append(site.sitemap, seedURL+path)
		site.pages[seedURL+path] = fakeSitePage{text: readable("en " + path)}
		for _, locale := range []string{"/de", "/vi", "/th"} {
			site.sitemap = append(site.sitemap, seedURL+locale+path)
			site.pages[seedURL+locale+path] = fakeSitePage{text: readable(locale + path)}
		}
	}
	site.sitemap = append(site.sitemap, seedURL+"/de/karriere")
	site.pages[seedURL+"/de/karriere"] = fakeSitePage{text: readable("/de/karriere")}

	crawl, err := testSiteCrawler(site).Crawl(context.Background(), seedURL)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, page := range crawl.Pages {
		got = append(got, page.URL)
	}
	want := []string{
		seedURL,
		seedURL + "/imprint", seedURL + "/about", // the probes lead
		seedURL + "/de/imprint", // the census's second entity chance, and its last
		seedURL + "/pricing", seedURL + "/de/karriere",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("wrong collapse: locale variants dedupe, legal pages bounded at %d:\n got %v\nwant %v", maxLegalLocalePages, got, want)
	}
	var legal int
	for _, page := range crawl.Pages {
		if page.Kind == crmcontracts.SiteReadPageKindImpressum {
			legal++
		}
	}
	if legal > maxLegalLocalePages {
		t.Fatalf("legal census read %d pages, want at most %d", legal, maxLegalLocalePages)
	}
}

func TestNormalizeCandidateStripsTrackingParamsSoVariantsDedupe(t *testing.T) {
	plain, ok := normalizeCandidate(seedURL + "/about")
	if !ok {
		t.Fatal("a plain URL failed to normalize")
	}
	tracked, ok := normalizeCandidate(seedURL + "/about?utm_source=nl&utm_campaign=x&fbclid=abc")
	if !ok {
		t.Fatal("a tracked URL failed to normalize")
	}
	if tracked != plain {
		t.Fatalf("tracking variants did not collapse: %q vs %q", tracked, plain)
	}
	kept, ok := normalizeCandidate(seedURL + "/about?lang=de")
	if !ok || kept != seedURL+"/about?lang=de" {
		t.Fatalf("a real query parameter was mangled: %q", kept)
	}
}

func waveFixtureSite() *fakeSite {
	site := &fakeSite{pages: seedOnly("/blog", "/pricing")}
	site.sitemap = []string{seedURL + "/cases"}
	for _, path := range []string{"/about", "/team", "/impressum", "/blog", "/pricing", "/cases", "/services"} {
		site.pages[seedURL+path] = fakeSitePage{text: readable(path)}
	}
	return site
}

// The frontier wave's replacement invariants for the old wave≡serial
// equivalence (frontier selection legitimately locks its choices in
// before later discoveries can compete): the crawl is deterministic
// across runs, commits follow selection order, and a wave of one still
// reproduces the serial walk.
func TestCrawlFrontierWavesAreDeterministicAcrossRuns(t *testing.T) {
	crawlOnce := func() siteCrawl {
		crawler := testSiteCrawler(waveFixtureSite())
		crawler.fetchWave = crawler.maxPages // production frontier sizing
		crawl, err := crawler.Crawl(context.Background(), seedURL)
		if err != nil {
			t.Fatal(err)
		}
		for i := range crawl.Pages {
			crawl.Pages[i].FetchDur = 0
		}
		return crawl
	}
	first := crawlOnce()
	for run := 0; run < 4; run++ {
		if again := crawlOnce(); !reflect.DeepEqual(first, again) {
			t.Fatalf("frontier crawl diverged between runs:\n%v\n%v", first, again)
		}
	}
}

func TestCrawlFrontierCommitsInSelectionOrder(t *testing.T) {
	crawler := testSiteCrawler(waveFixtureSite())
	crawler.fetchWave = crawler.maxPages
	crawl, err := crawler.Crawl(context.Background(), seedURL)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, page := range crawl.Pages {
		order = append(order, page.URL)
	}
	// Selection order: seed, probes (impressum/about/team/services), then
	// sitemap+links by kind priority, boilerplate blog last.
	want := []string{
		seedURL, seedURL + "/impressum", seedURL + "/about", seedURL + "/team", seedURL + "/services",
		seedURL + "/cases", seedURL + "/pricing", seedURL + "/blog",
	}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("commit order = %v, want selection order %v", order, want)
	}
}

func TestCrawlWaveOfOneReproducesTheSerialWalk(t *testing.T) {
	serial, err := testSiteCrawler(waveFixtureSite()).Crawl(context.Background(), seedURL)
	if err != nil {
		t.Fatal(err)
	}
	again, err := testSiteCrawler(waveFixtureSite()).Crawl(context.Background(), seedURL)
	if err != nil {
		t.Fatal(err)
	}
	for i := range serial.Pages {
		serial.Pages[i].FetchDur = 0
	}
	for i := range again.Pages {
		again.Pages[i].FetchDur = 0
	}
	if !reflect.DeepEqual(serial, again) {
		t.Fatalf("the wave-of-one walk is not stable:\n%v\n%v", serial, again)
	}
}

func TestCrawlStopsWhenTheClockRunsOut(t *testing.T) {
	site := &fakeSite{pages: seedOnly()}
	site.sitemap = []string{seedURL + "/never-reached"}
	site.pages[seedURL+"/never-reached"] = fakeSitePage{text: readable("late")}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The clock "runs out" right after the seed fetch — cancellation stands
	// in for the wall deadline, no real waiting involved.
	site.onFetch = func(string) { cancel() }

	crawl, err := testSiteCrawler(site).Crawl(ctx, seedURL)
	if err != nil {
		t.Fatal(err)
	}
	if len(crawl.Pages) != 1 {
		t.Fatalf("fetched %d pages after the deadline, want only the seed", len(crawl.Pages))
	}
	if crawl.Stopped == nil || *crawl.Stopped != crmcontracts.SiteReadReportStoppedReasonDeadline {
		t.Fatalf("Stopped = %v, want deadline", crawl.Stopped)
	}
}
