// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The deep site read's crawler: a bounded, same-site walk under the
// founder-ratified R2 caps (12 pages, 8 MiB, 90 s). Discovery is
// DETERMINISTIC GO ONLY — well-known paths, then sitemap.xml, then nav links,
// in that order — never model-chosen, so page content can influence at most
// WHICH same-site links exist, never talk the crawl into leaving the site or
// raising its budget. The crawler only fetches; extraction stays with the
// caller.

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/webread"
)

const (
	crawlMaxPages = 12
	crawlMaxBytes = 8 << 20
	crawlWall     = 90 * time.Second
	// crawlSkipReportCap bounds how many left-behind candidates a cap stop
	// records: enough to show what was cut, without a 5000-URL sitemap
	// ballooning the report.
	crawlSkipReportCap = 20
	// crawlMinRunes is the floor under which a fetched page carries no
	// extractable prose — a bare redirect stub or cookie wall.
	crawlMinRunes = 80
)

type crawlPage struct {
	URL  string
	Kind crmcontracts.SiteReadPageKind
	Text string
}

type crawlSkip struct {
	URL    string
	Reason crmcontracts.SiteReadSkipReason
}

type siteCrawl struct {
	Pages   []crawlPage
	Skipped []crawlSkip
	Stopped *crmcontracts.SiteReadReportStoppedReason // nil = discovery exhausted
}

// siteFetcher is the slice of *webread.Fetcher the crawler needs; tests feed
// an in-memory site through it.
type siteFetcher interface {
	FetchPage(ctx context.Context, rawURL string) (webread.Page, error)
	FetchSitemap(ctx context.Context, origin string) ([]string, error)
}

// crawlPacer is the per-crawl politeness seam (*webread.Pacer in production).
type crawlPacer interface {
	Wait(ctx context.Context) error
	Done()
}

type siteCrawler struct {
	fetch    siteFetcher
	newPacer func() crawlPacer
	maxPages int
	maxBytes int
	wall     time.Duration
}

func newSiteCrawler(fetch siteFetcher) *siteCrawler {
	return &siteCrawler{
		fetch:    fetch,
		newPacer: func() crawlPacer { return webread.NewPacer() },
		maxPages: crawlMaxPages,
		maxBytes: crawlMaxBytes,
		wall:     crawlWall,
	}
}

// wellKnownProbes are the paths tried on the seed's origin before any
// discovered URL — each a guess at where sites keep the page of that kind.
// Order is the fetch order; at most one page per kind is taken from probes,
// so a hit on /impressum spares the /imprint and /de/impressum guesses.
var wellKnownProbes = []struct {
	path string
	kind crmcontracts.SiteReadPageKind
}{
	{"/impressum", crmcontracts.SiteReadPageKindImpressum},
	{"/imprint", crmcontracts.SiteReadPageKindImpressum},
	{"/de/impressum", crmcontracts.SiteReadPageKindImpressum},
	{"/legal-notice", crmcontracts.SiteReadPageKindImpressum},
	{"/about", crmcontracts.SiteReadPageKindAbout},
	{"/ueber-uns", crmcontracts.SiteReadPageKindAbout},
	{"/team", crmcontracts.SiteReadPageKindTeam},
	{"/kontakt", crmcontracts.SiteReadPageKindContact},
	{"/contact", crmcontracts.SiteReadPageKindContact},
	{"/services", crmcontracts.SiteReadPageKindServices},
	{"/leistungen", crmcontracts.SiteReadPageKindServices},
	{"/products", crmcontracts.SiteReadPageKindProducts},
	{"/produkte", crmcontracts.SiteReadPageKindProducts},
}

// crawlCandidate is one URL the deterministic discovery proposed.
type crawlCandidate struct {
	url  string
	kind crmcontracts.SiteReadPageKind // set for probes; "" = classify from path
	// probe marks a well-known guess: its kind participates in the
	// one-page-per-kind preference, and it exists only because we invented
	// the path.
	probe bool
}

// Crawl walks the seed's site breadth-first under the caps. The seed page
// failing is a failed crawl, not a partial one — every later loss degrades to
// a recorded skip or an early stop instead.
func (c *siteCrawler) Crawl(ctx context.Context, seedURL string) (siteCrawl, error) {
	ctx, cancel := context.WithTimeout(ctx, c.wall)
	defer cancel()
	pacer := c.newPacer()

	seedPage, err := c.fetchPaced(ctx, pacer, seedURL)
	if err != nil {
		return siteCrawl{}, fmt.Errorf("site read of %s: the seed page itself failed: %w", seedURL, err)
	}
	seedParsed, err := url.Parse(seedURL)
	if err != nil {
		return siteCrawl{}, fmt.Errorf("site read: %q is not a crawlable URL: %w", seedURL, err)
	}

	run := newCrawlRun(c, pacer, seedURL, seedPage)
	run.discover(ctx, seedParsed.Scheme+"://"+seedParsed.Host, seedPage)
	for i := 0; i < len(run.queue); i++ {
		if stop := stopReason(ctx, len(run.crawl.Pages), c.maxPages, run.totalBytes, c.maxBytes); stop != nil {
			run.crawl.Stopped = stop
			run.crawl.Skipped = append(run.crawl.Skipped, leftBehind(run.queue[i:], run.visited, *stop)...)
			break
		}
		run.visit(ctx, run.queue[i])
		if run.crawl.Stopped != nil {
			break // the clock ran out mid-fetch
		}
	}
	return run.crawl, nil // Stopped nil = discovery exhausted, the natural end
}

// fetchPaced is one polite fetch: pacer slot in, fetch, slot out.
func (c *siteCrawler) fetchPaced(ctx context.Context, pacer crawlPacer, rawURL string) (webread.Page, error) {
	if err := pacer.Wait(ctx); err != nil {
		return webread.Page{}, err
	}
	defer pacer.Done()
	return c.fetch.FetchPage(ctx, rawURL)
}

// crawlRun is one crawl's working state: the report being built plus the
// dedupe sets that keep the walk from re-reading anything.
type crawlRun struct {
	crawler *siteCrawler
	pacer   crawlPacer
	seedURL string

	crawl         siteCrawl
	queue         []crawlCandidate
	visited       map[string]bool
	seenText      map[string]bool
	probeKindDone map[crmcontracts.SiteReadPageKind]bool
	totalBytes    int
}

func newCrawlRun(c *siteCrawler, pacer crawlPacer, seedURL string, seedPage webread.Page) *crawlRun {
	visited := map[string]bool{seedURL: true}
	if normalizedSeed, ok := normalizeCandidate(seedURL); ok {
		// Nav links back to the landing page arrive normalized; both
		// spellings of the seed are the page already read.
		visited[normalizedSeed] = true
	}
	return &crawlRun{
		crawler: c,
		pacer:   pacer,
		seedURL: seedURL,
		crawl: siteCrawl{
			Pages: []crawlPage{{URL: seedURL, Kind: crmcontracts.SiteReadPageKindHome, Text: seedPage.Text}},
		},
		visited:       visited,
		seenText:      map[string]bool{seedPage.Text: true},
		probeKindDone: map[crmcontracts.SiteReadPageKind]bool{},
		totalBytes:    seedPage.Bytes,
	}
}

// discover seeds the candidate queue in the documented deterministic order:
// well-known probes on the seed's origin, then sitemap <loc>s, then the seed
// page's own nav links. Later fetches append THEIR links breadth-first.
func (r *crawlRun) discover(ctx context.Context, origin string, seedPage webread.Page) {
	for _, probe := range wellKnownProbes {
		r.queue = append(r.queue, crawlCandidate{url: origin + probe.path, kind: probe.kind, probe: true})
	}
	sitemapLocs, err := r.crawler.fetch.FetchSitemap(ctx, origin)
	if err != nil {
		// The sitemap is one of three discovery channels, and the flakiest:
		// SPA catch-alls serve HTML at /sitemap.xml, robots may fence it off.
		// Losing it narrows discovery; it must not fail a read that the
		// probes and nav links can still carry.
		sitemapLocs = nil
	}
	for _, loc := range sitemapLocs {
		r.queue = append(r.queue, crawlCandidate{url: loc})
	}
	r.queue = append(r.queue, linkCandidates(seedPage.Links)...)
}

// visit handles one candidate: gate it, fetch it, file the outcome as a page,
// a recorded skip, a silent skip, or — when the clock runs out mid-fetch —
// the crawl's deadline stop.
func (r *crawlRun) visit(ctx context.Context, cand crawlCandidate) {
	candURL, ok := normalizeCandidate(cand.url)
	if !ok || r.visited[candURL] {
		return
	}
	r.visited[candURL] = true
	if cand.probe && r.probeKindDone[cand.kind] {
		// A probe of an already-satisfied kind: skipped without a report
		// entry — the path was our guess, not a page the site offered.
		return
	}
	if strings.HasSuffix(strings.ToLower(candURL), ".xml") {
		// A sitemapindex's <loc>s are child sitemaps; the crawl deliberately
		// does not recurse into them (bounded discovery), and an XML file is
		// not a readable page either way.
		return
	}
	if !webread.SameRegistrableDomain(r.seedURL, candURL) {
		// The security property of the whole crawler: no page content can
		// send the crawl off the seed's site — off-domain candidates are
		// recorded, never fetched.
		r.skip(candURL, crmcontracts.OffDomain)
		return
	}

	page, err := r.crawler.fetchPaced(ctx, r.pacer, candURL)
	switch {
	case errors.Is(err, webread.ErrRobotsDisallowed):
		r.skip(candURL, crmcontracts.Robots)
		return
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled):
		// The crawl's clock ran out mid-fetch; the loop head would catch it
		// one iteration later, this just stops without a bogus per-page
		// "unreadable".
		r.crawl.Stopped = stoppedPtr(crmcontracts.SiteReadReportStoppedReasonDeadline)
		return
	case err != nil:
		r.skip(candURL, crmcontracts.Unreadable)
		return
	}
	if utf8.RuneCountInString(page.Text) < crawlMinRunes {
		r.skip(candURL, crmcontracts.Unreadable)
		return
	}
	if r.seenText[page.Text] {
		// An SPA catch-all serves the same document on every path; the
		// duplicate carries zero new evidence and reporting it as a skip
		// would flood the report with noise, so it vanishes silently.
		return
	}

	r.seenText[page.Text] = true
	r.totalBytes += page.Bytes
	kind := cand.kind
	if kind == "" {
		kind = classifyKind(candURL)
	}
	if cand.probe {
		r.probeKindDone[cand.kind] = true
	}
	r.crawl.Pages = append(r.crawl.Pages, crawlPage{URL: candURL, Kind: kind, Text: page.Text})
	r.queue = append(r.queue, linkCandidates(page.Links)...)
}

func (r *crawlRun) skip(candURL string, reason crmcontracts.SiteReadSkipReason) {
	r.crawl.Skipped = append(r.crawl.Skipped, crawlSkip{URL: candURL, Reason: reason})
}

// stopReason answers whether a cap ends the crawl before the next fetch.
func stopReason(ctx context.Context, pages, maxPages, bytes, maxBytes int) *crmcontracts.SiteReadReportStoppedReason {
	switch {
	case ctx.Err() != nil:
		return stoppedPtr(crmcontracts.SiteReadReportStoppedReasonDeadline)
	case pages >= maxPages:
		return stoppedPtr(crmcontracts.SiteReadReportStoppedReasonPageCap)
	case bytes >= maxBytes:
		return stoppedPtr(crmcontracts.SiteReadReportStoppedReasonByteCap)
	default:
		return nil
	}
}

// leftBehind records the candidates a page/byte cap cut off, capped at
// crawlSkipReportCap and carrying the cap's own reason. A deadline stop
// records nothing: time running out says nothing about individual pages.
func leftBehind(rest []crawlCandidate, visited map[string]bool, stop crmcontracts.SiteReadReportStoppedReason) []crawlSkip {
	var reason crmcontracts.SiteReadSkipReason
	switch stop {
	case crmcontracts.SiteReadReportStoppedReasonPageCap:
		reason = crmcontracts.PageCap
	case crmcontracts.SiteReadReportStoppedReasonByteCap:
		reason = crmcontracts.ByteCap
	default:
		return nil
	}
	var skips []crawlSkip
	for _, cand := range rest {
		if len(skips) == crawlSkipReportCap {
			break
		}
		candURL, ok := normalizeCandidate(cand.url)
		if !ok || visited[candURL] {
			continue
		}
		visited[candURL] = true
		skips = append(skips, crawlSkip{URL: candURL, Reason: reason})
	}
	return skips
}

// normalizeCandidate reduces a discovered URL to its fetchable identity:
// absolute http(s), fragment dropped (a fragment names a position, not a
// different document).
func normalizeCandidate(rawURL string) (string, bool) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != schemeHTTP && parsed.Scheme != schemeHTTPS) {
		return "", false
	}
	parsed.Fragment = ""
	return parsed.String(), true
}

func linkCandidates(links []string) []crawlCandidate {
	cands := make([]crawlCandidate, 0, len(links))
	for _, link := range links {
		cands = append(cands, crawlCandidate{url: link})
	}
	return cands
}

// classifyKind names what a discovered page probably is, from its path alone.
// Keyword order mirrors the probe list; the first family that matches wins.
func classifyKind(rawURL string) crmcontracts.SiteReadPageKind {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return crmcontracts.SiteReadPageKindOther
	}
	path := strings.ToLower(parsed.Path)
	switch {
	case containsAny(path, "impressum", "imprint", "legal"):
		return crmcontracts.SiteReadPageKindImpressum
	case containsAny(path, "about", "ueber"):
		return crmcontracts.SiteReadPageKindAbout
	case strings.Contains(path, "team"):
		return crmcontracts.SiteReadPageKindTeam
	case containsAny(path, "kontakt", "contact"):
		return crmcontracts.SiteReadPageKindContact
	case containsAny(path, "service", "leistung"):
		return crmcontracts.SiteReadPageKindServices
	case containsAny(path, "produkt", "product"):
		return crmcontracts.SiteReadPageKindProducts
	default:
		return crmcontracts.SiteReadPageKindOther
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func stoppedPtr(reason crmcontracts.SiteReadReportStoppedReason) *crmcontracts.SiteReadReportStoppedReason {
	return &reason
}
