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
	"net"
	"net/url"
	"time"

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

// The crawl fetches in bounded WAVES: each round takes the best few
// admissible candidates and fetches them concurrently (the webread
// pacer's in-flight budget does the throttling), and results COMMIT
// strictly in selection order, so the walk stays deterministic whatever
// order the responses arrive in. Tests pin the wave to 1 so their
// in-memory fetch logs stay sequential.

// crawlWaveSize bounds one concurrent round. It is deliberately far below
// the page budget: a wave as wide as the budget spends every slot on the
// candidates known at the seed, so a page discovered one link deeper — a
// solutions index linked only from a submenu — can never compete however
// well it ranks. Re-ranking every few pages lets discovery correct the
// order, and the staggered commits are what the SPA's progress counter
// actually watches. Small enough to re-rank often, wide enough that the
// pacer, not this bound, remains the throughput limit.
const crawlWaveSize = 6

type siteCrawler struct {
	fetch     siteFetcher
	newPacer  func() crawlPacer
	maxPages  int
	maxBytes  int
	wall      time.Duration
	fetchWave int
}

func newSiteCrawler(fetch siteFetcher, caps CrawlCaps) *siteCrawler {
	caps = caps.withDefaults()
	return &siteCrawler{
		fetch:     fetch,
		newPacer:  func() crawlPacer { return webread.NewPacer() },
		maxPages:  caps.MaxPages,
		maxBytes:  caps.MaxBytes,
		wall:      caps.Wall,
		fetchWave: crawlWaveSize,
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
	{"/impressum.html", crmcontracts.SiteReadPageKindImpressum},
	{"/imprint", crmcontracts.SiteReadPageKindImpressum},
	{"/de/impressum", crmcontracts.SiteReadPageKindImpressum},
	{"/en/imprint", crmcontracts.SiteReadPageKindImpressum},
	{"/en/legal-notice", crmcontracts.SiteReadPageKindImpressum},
	{"/legal-notice", crmcontracts.SiteReadPageKindImpressum},
	{"/de/publisher", crmcontracts.SiteReadPageKindImpressum},
	{"/publisher", crmcontracts.SiteReadPageKindImpressum},
	{"/de/legal", crmcontracts.SiteReadPageKindImpressum},
	{"/c/legal", crmcontracts.SiteReadPageKindImpressum},
	{"/legal", crmcontracts.SiteReadPageKindImpressum},
	// Some sites publish no identity page; one policy document is the
	// bounded fallback because its publisher block still identifies the
	// contracting entity. Probe one only — never crawl the whole library.
	{"/en/terms-of-service", crmcontracts.SiteReadPageKindImpressum},
	{"/legal/terms", crmcontracts.SiteReadPageKindImpressum},
	{"/legal/terms-of-service", crmcontracts.SiteReadPageKindImpressum},
	{"/legal/aup", crmcontracts.SiteReadPageKindImpressum},
	{"/about", crmcontracts.SiteReadPageKindAbout},
	{"/en/about", crmcontracts.SiteReadPageKindAbout},
	{"/ueber-uns", crmcontracts.SiteReadPageKindAbout},
	{"/team", crmcontracts.SiteReadPageKindTeam},
	{"/kontakt", crmcontracts.SiteReadPageKindContact},
	{"/contact", crmcontracts.SiteReadPageKindContact},
	{"/en/contact", crmcontracts.SiteReadPageKindContact},
	{"/services", crmcontracts.SiteReadPageKindServices},
	{"/en/services", crmcontracts.SiteReadPageKindServices},
	{"/solutions", crmcontracts.SiteReadPageKindServices},
	{"/en/solutions", crmcontracts.SiteReadPageKindServices},
	{"/leistungen", crmcontracts.SiteReadPageKindServices},
	{"/products", crmcontracts.SiteReadPageKindProducts},
	{"/en/products", crmcontracts.SiteReadPageKindProducts},
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
	return c.CrawlStream(ctx, seedURL, nil)
}

// CrawlStream is Crawl with a per-commit hook: onPage fires serially,
// in commit order, the moment a page joins the result — the seam that
// lets extraction START while the crawl still runs. A nil hook is Crawl.
func (c *siteCrawler) CrawlStream(ctx context.Context, seedURL string, onPage func(crawlPage)) (siteCrawl, error) {
	ctx, cancel := context.WithTimeout(ctx, c.wall)
	defer cancel()
	pacer := c.newPacer()

	seedPage, err := c.fetchPaced(ctx, pacer, seedURL)
	if transientCrawlError(ctx, err) {
		// The landing page is the only irreplaceable discovery source. One
		// immediate retry absorbs a transient edge/CDN timeout while the crawl's
		// wall deadline still bounds the attempt.
		seedPage, err = c.fetchPaced(ctx, pacer, seedURL)
	}
	if err != nil {
		return siteCrawl{}, fmt.Errorf("site read of %s: the seed page itself failed: %w", seedURL, err)
	}
	seedParsed, err := url.Parse(seedURL)
	if err != nil {
		return siteCrawl{}, fmt.Errorf("site read: %q is not a crawlable URL: %w", seedURL, err)
	}

	run := newCrawlRun(c, pacer, seedURL, seedPage)
	run.onPage = onPage
	if onPage != nil {
		onPage(run.crawl.Pages[0]) // the seed page is committed already
	}
	run.discover(ctx, seedParsed.Scheme+"://"+seedParsed.Host, seedPage)
	// Highest-priority-first selection in concurrent WAVES: each round
	// picks the best untaken candidates (bounded by the fetch wave and
	// the remaining page budget), screens them serially, fetches them
	// concurrently through the pacer, then commits the results strictly
	// in selection order — dedupe, caps, page append and link discovery
	// all run single-threaded, so the walk stays deterministic whatever
	// order the responses arrive in. Ties break on insertion order and
	// every priority is computed from the URL alone.
	var taken []bool
	var priority []int
	for {
		for len(priority) < len(run.queue) {
			priority = append(priority, candidatePriority(run.queue[len(priority)]))
			taken = append(taken, false)
		}
		if stop := stopReason(ctx, len(run.crawl.Pages), c.maxPages, run.totalBytes, c.maxBytes); stop != nil {
			run.crawl.Stopped = stop
			run.crawl.Skipped = append(run.crawl.Skipped, leftBehind(untakenCandidates(run.queue, taken), run.visited, *stop)...)
			break
		}
		waveMax := c.fetchWave
		if remaining := c.maxPages - len(run.crawl.Pages); remaining < waveMax {
			waveMax = remaining
		}
		wave := selectWave(run.queue, priority, taken, waveMax)
		if len(wave) == 0 {
			break // discovery exhausted, the natural end
		}
		admitted := make([]admission, 0, len(wave))
		for _, cand := range wave {
			if adm, ok := run.admit(cand); ok {
				admitted = append(admitted, adm)
			}
		}
		results := run.fetchWave(ctx, admitted)
		for i, adm := range admitted {
			run.commit(ctx, adm, results[i])
			if run.crawl.Stopped != nil {
				break
			}
		}
		if run.crawl.Stopped != nil {
			// A mid-commit stop (deadline, byte cap) never reaches the loop
			// head again; the cut-off remainder is recorded here.
			run.crawl.Skipped = append(run.crawl.Skipped, leftBehind(untakenCandidates(run.queue, taken), run.visited, *run.crawl.Stopped)...)
			break
		}
	}
	run.crawl.TotalBytes = run.totalBytes
	return run.crawl, nil
}

func transientCrawlError(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var networkError net.Error
	return errors.As(err, &networkError) && networkError.Timeout()
}

// selectWave marks and returns the next wave: the highest-priority
// untaken candidates, insertion order breaking ties, up to waveMax.
func selectWave(queue []crawlCandidate, priority []int, taken []bool, waveMax int) []crawlCandidate {
	var wave []crawlCandidate
	for len(wave) < waveMax {
		next := -1
		for i := range queue {
			if taken[i] {
				continue
			}
			if next == -1 || priority[i] > priority[next] {
				next = i
			}
		}
		if next == -1 {
			break
		}
		taken[next] = true
		wave = append(wave, queue[next])
	}
	return wave
}

// untakenCandidates lists what the selection never reached, in insertion
// order — the input to the cap-stop skip report.
func untakenCandidates(queue []crawlCandidate, taken []bool) []crawlCandidate {
	var rest []crawlCandidate
	for i, cand := range queue {
		// A commit can discover new links and hit the byte/deadline cap in
		// the same wave. Those links have joined queue but the next loop-head
		// has not extended taken yet; they are necessarily untaken.
		if i >= len(taken) || !taken[i] {
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
	onPage        func(crawlPage)
	queue         []crawlCandidate
	visited       map[string]bool
	seenText      map[string]bool
	canonicalDone map[string]bool
	probeKindDone map[crmcontracts.SiteReadPageKind]bool
	// impressumRead counts committed legal pages: the locale bypass that
	// keeps the entity census honest is bounded by it (legalCensusOpen).
	impressumRead int
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
		canonicalDone: map[string]bool{localeCanonical(seedURL): true},
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
	if transientCrawlError(ctx, err) {
		// A single slow sitemap response must not collapse an SPA site to its
		// landing page. Retry once without delaying; the crawl's wall deadline
		// remains the governing bound.
		sitemapLocs, err = r.crawler.fetch.FetchSitemap(ctx, origin)
	}
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
		reason = crmcontracts.SiteReadSkipReasonPageCap
	case crmcontracts.SiteReadReportStoppedReasonByteCap:
		reason = crmcontracts.SiteReadSkipReasonByteCap
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
