// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The deep site read's crawler: a bounded, same-site walk under
// operator-tunable caps (CrawlCaps; the defaults below). Discovery is
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
	defaultCrawlMaxPages = 40
	defaultCrawlMaxBytes = 32 << 20
	defaultCrawlWall     = 240 * time.Second
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
	// Bytes and FetchDur are observability for the debug report; the
	// pipeline itself keys off Text alone.
	Bytes    int
	FetchDur time.Duration
}

type crawlSkip struct {
	URL    string
	Reason crmcontracts.SiteReadSkipReason
}

type siteCrawl struct {
	Pages      []crawlPage
	Skipped    []crawlSkip
	Stopped    *crmcontracts.SiteReadReportStoppedReason // nil = discovery exhausted
	TotalBytes int
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

// CrawlCaps bounds one deep read. The zero value means "the defaults":
// operators only ever tighten or widen the caps deliberately, and a
// caller that has no opinion inherits the ratified defaults.
type CrawlCaps struct {
	MaxPages int
	MaxBytes int
	Wall     time.Duration
}

func (c CrawlCaps) withDefaults() CrawlCaps {
	if c.MaxPages <= 0 {
		c.MaxPages = defaultCrawlMaxPages
	}
	if c.MaxBytes <= 0 {
		c.MaxBytes = defaultCrawlMaxBytes
	}
	if c.Wall <= 0 {
		c.Wall = defaultCrawlWall
	}
	return c
}

type siteCrawler struct {
	fetch    siteFetcher
	newPacer func() crawlPacer
	maxPages int
	maxBytes int
	wall     time.Duration
}

func newSiteCrawler(fetch siteFetcher, caps CrawlCaps) *siteCrawler {
	caps = caps.withDefaults()
	return &siteCrawler{
		fetch:    fetch,
		newPacer: func() crawlPacer { return webread.NewPacer() },
		maxPages: caps.MaxPages,
		maxBytes: caps.MaxBytes,
		wall:     caps.Wall,
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
	// Highest-priority-first selection instead of FIFO: the page budget
	// goes to the kinds that state facts (legal, about, team, contact,
	// offerings) before generic nav links, and boilerplate archives only
	// fill leftover budget. Ties break on insertion order and every
	// priority is computed from the URL alone, so the walk stays
	// deterministic.
	var taken []bool
	var priority []int
	for {
		for len(priority) < len(run.queue) {
			priority = append(priority, candidatePriority(run.queue[len(priority)]))
			taken = append(taken, false)
		}
		next := -1
		for i := range run.queue {
			if taken[i] {
				continue
			}
			if next == -1 || priority[i] > priority[next] {
				next = i
			}
		}
		if next == -1 {
			break // discovery exhausted, the natural end
		}
		if stop := stopReason(ctx, len(run.crawl.Pages), c.maxPages, run.totalBytes, c.maxBytes); stop != nil {
			run.crawl.Stopped = stop
			run.crawl.Skipped = append(run.crawl.Skipped, leftBehind(untakenCandidates(run.queue, taken), run.visited, *stop)...)
			break
		}
		taken[next] = true
		run.visit(ctx, run.queue[next])
		if run.crawl.Stopped != nil {
			break // the clock ran out mid-fetch
		}
	}
	run.crawl.TotalBytes = run.totalBytes
	return run.crawl, nil
}

// untakenCandidates lists what the selection never reached, in insertion
// order — the input to the cap-stop skip report.
func untakenCandidates(queue []crawlCandidate, taken []bool) []crawlCandidate {
	var rest []crawlCandidate
	for i, cand := range queue {
		if !taken[i] {
			rest = append(rest, cand)
		}
	}
	return rest
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
			Pages: []crawlPage{{URL: seedURL, Kind: crmcontracts.SiteReadPageKindHome, Text: seedPage.Text, Bytes: seedPage.Bytes}},
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

	r.fetchAndRecord(ctx, cand, candURL)
}

// fetchAndRecord is visit's tail once the candidate is admissible (same-site,
// not a duplicate probe, not an XML index): fetch it, and either record a skip
// with its reason or append the page and enqueue its links.
func (r *crawlRun) fetchAndRecord(ctx context.Context, cand crawlCandidate, candURL string) {
	fetchStart := time.Now()
	page, err := r.crawler.fetchPaced(ctx, r.pacer, candURL)
	fetchDur := time.Since(fetchStart)
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

	// The pre-fetch stopReason bounds the crawl by the PREVIOUS total; this
	// page, already fetched, must not push the aggregate past the advertised
	// cap. Over-cap → record it as a byte_cap skip and stop, rather than
	// silently exceeding the byte budget the report promises.
	if r.totalBytes+page.Bytes > r.crawler.maxBytes {
		r.skip(candURL, crmcontracts.ByteCap)
		r.crawl.Stopped = stoppedPtr(crmcontracts.SiteReadReportStoppedReasonByteCap)
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
	r.crawl.Pages = append(r.crawl.Pages, crawlPage{URL: candURL, Kind: kind, Text: page.Text, Bytes: page.Bytes, FetchDur: fetchDur})
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

func linkCandidates(links []string) []crawlCandidate {
	cands := make([]crawlCandidate, 0, len(links))
	for _, link := range links {
		cands = append(cands, crawlCandidate{url: link})
	}
	return cands
}

func stoppedPtr(reason crmcontracts.SiteReadReportStoppedReason) *crmcontracts.SiteReadReportStoppedReason {
	return &reason
}
