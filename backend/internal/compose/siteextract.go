// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The deep read's extraction orchestration: the page-fact fan-out and
// the profile call run CONCURRENTLY — their wall clock is the product's
// read time. The profile lane runs at most TWICE: once as soon as there
// is enough evidence to answer at all, and again over the finished crawl
// when the corpus grew enough that the first answer was drawn from a
// fraction of the site (reprofileOverWholeCrawl).
//
// Collect-don't-cancel: one page's failure costs that page's findings and
// degrades the read to partial, never the whole fan-out; the worker and the
// debug CLI share this exact spine.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/ai"
)

// pageExtractConcurrency bounds the fan-out. The calls are tiny and the
// read's wall clock IS their slowest round, so the bound is generous —
// effectively "every fact-bearing page at once" for a capped crawl —
// while still capping runaway parallelism against provider rate limits
// and the worker's DB pool (each call meters through it).
const pageExtractConcurrency = 40

// siteExtraction is the fan-out's outcome: the gated profile fields,
// the merged per-page findings, and the joined error of whatever lanes
// failed (nil = everything completed).
type siteExtraction struct {
	fields []evidencedField
	merged pageFactsResult
	err    error
	// legalCensusIncomplete marks that a LEGAL page's fact call failed:
	// its entities never voted, so the multi-entity abstention cannot
	// trust the census — the legal trio is withheld rather than staged
	// on a possibly-undercounted count.
	legalCensusIncomplete bool
	// crawlMs is the crawl's own share of the overlapped run (extraction
	// keeps going after the crawl returns).
	crawlMs int64
}

// profileTriggerNonLegalPages is how much commercial evidence the profile
// lane waits for before firing. A raw page-count trigger can be satisfied
// entirely by legal pages on policy-heavy sites, permanently producing a
// legal-only profile. A site that never grows enough to warrant the second
// pass ships that answer, so the trigger counts commercial pages rather than
// pages.
const profileTriggerNonLegalPages = 8

func profileEvidenceReady(pages []crawlPage) bool {
	nonLegal := 0
	for _, page := range pages {
		if page.Kind != crmcontracts.SiteReadPageKindImpressum {
			nonLegal++
		}
	}
	return nonLegal >= profileTriggerNonLegalPages
}

// The read's two live phases, spelled as the site_read store accepts them
// (people.Store.UpdateSiteReadProgress rejects anything else): the crawl
// is still fetching, or the crawl is done and the model lanes are not.
const (
	sitePhaseCrawling   = "crawling"
	sitePhaseExtracting = "extracting"
)

// crawlAndExtract OVERLAPS the crawl and the extraction: page-fact
// calls launch the moment their page commits, and the profile call
// fires once the top-ranked pages are in (or the crawl ends, whichever
// is first). The read's wall clock becomes ~max(crawl, slowest lane) instead
// of their sum, PLUS the one re-profile call when the crawl grew enough to
// warrant it (reprofileOverWholeCrawl) — that one is serial, because it reads
// the finished corpus. onProgress (may be nil) fires serially with the live
// phase and the pages fetched so far.
func crawlAndExtract(ctx context.Context, crawler *siteCrawler, x evidenceExtractor, seedURL string, onProgress func(phase string, pages []crawlPage), onDraft func(pageFactsResult)) (siteCrawl, siteExtraction, error) {
	var out siteExtraction
	collected := pageFactsCollector{onDraft: onDraft}
	progress := func(phase string, pages []crawlPage) {
		if onProgress != nil {
			onProgress(phase, pages)
		}
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, pageExtractConcurrency)
	var committed []crawlPage
	var committedMu sync.Mutex

	profileOnce := sync.Once{}
	var profileWg sync.WaitGroup
	var profileErr error
	var profileMu sync.Mutex
	profiledPages := 0
	fireProfile := func() {
		profileOnce.Do(func() {
			snapshot := snapshotCrawlPages(&committedMu, &committed)
			profileMu.Lock()
			profiledPages = len(snapshot)
			profileMu.Unlock()
			profileWg.Add(1)
			go func() {
				defer profileWg.Done()
				fields, err := safeExtractProfile(ctx, x, snapshot)
				profileMu.Lock()
				out.fields, profileErr = fields, err
				profileMu.Unlock()
			}()
		})
	}

	crawlStart := time.Now()
	crawl, crawlErr := crawler.CrawlStream(ctx, seedURL, func(page crawlPage) {
		committedMu.Lock()
		committed = append(committed, page)
		pages := append([]crawlPage(nil), committed...)
		profileReady := profileEvidenceReady(committed)
		committedMu.Unlock()
		// Report the page the moment it commits, not when its extraction
		// returns: "pages read" is a count of pages fetched, and the hook
		// runs serially in commit order, so the number only ever climbs.
		progress(sitePhaseCrawling, pages)
		if profileReady {
			fireProfile()
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			res, err := safeExtractPageFacts(ctx, x, page)
			collected.record(page, res, err)
		}()
	})
	out.crawlMs = time.Since(crawlStart).Milliseconds()
	if crawlErr != nil {
		// CrawlStream errors only at the seed page, BEFORE any onPage
		// fires — no page goroutine or profile lane exists yet; the Waits
		// are a belt against that invariant ever loosening (a leaked
		// profile goroutine would be an unawaited metered model call).
		wg.Wait()
		profileWg.Wait()
		return siteCrawl{}, siteExtraction{}, crawlErr
	}
	// The crawl is done but its pages' extraction lanes are not: hold the
	// page count and move the phase, so the SPA stops showing "discovering"
	// while the model is the only thing still working.
	progress(sitePhaseExtracting, append([]crawlPage(nil), crawl.Pages...))
	fireProfile() // a small crawl may end below the trigger
	wg.Wait()
	profileWg.Wait()

	// A first pass that failed for budget means the workspace is over its
	// threshold: asking again, with a LARGER prompt, spends against an
	// allowance that is already exhausted.
	fields, recovered := reprofileOverWholeCrawl(
		ctx, x, crawl.Pages, profiledPages, out.fields, profileErr)
	out.fields = fields
	if profileErr != nil && !recovered {
		collected.failed = append(collected.failed, fmt.Errorf("profile lane: %w", profileErr))
	}
	out.legalCensusIncomplete = collected.legalCensusIncomplete
	out.merged = mergeInCommitOrder(crawl, collected.results)
	out.err = errors.Join(collected.failed...)
	return crawl, out, nil
}

// profileGrowthFactor is how much bigger the corpus must be before the
// profile is worth asking again. Below it the second call would see nearly
// the same evidence and cost a model call to confirm the first answer.
const profileGrowthFactor = 2

// reprofileOverWholeCrawl asks the profile lane again once the crawl has
// finished, and answers the profile to keep.
//
// The first call ran on the pages that existed at the trigger, which on a
// large site is a fraction of what the crawl went on to find: a 40-page site
// was profiled from its first 8, so the About, team and services pages that
// arrive later were never read.
//
// A failure here is LOGGED, never joined into the read's errors. This call is
// optional refinement over an answer already in hand, and `extraction.err`
// decides both the read's partial status and whether deferForBudget re-queues
// it — so joining it would let a bonus call that tripped the budget defer a
// read whose first pass had already grounded every field.
func reprofileOverWholeCrawl(
	ctx context.Context, x evidenceExtractor, pages []crawlPage, profiled int,
	current []evidencedField, firstErr error,
) (merged []evidencedField, recoveredFirstPass bool) {
	if profiled == 0 || len(pages) < profiled*profileGrowthFactor {
		return current, false
	}
	if firstErr != nil && errors.As(firstErr, new(*ai.BudgetDeferralError)) {
		// The workspace is over its token threshold. A second, larger call
		// against the same exhausted budget buys nothing and defers again.
		return current, false
	}
	fields, err := safeExtractProfile(ctx, x, pages)
	if err != nil {
		slog.WarnContext(ctx, "the profile re-run failed; keeping the first pass",
			"pages", len(pages), "profiled_at_trigger", profiled, "err", err)
		return current, false
	}
	// `fields` is the RE-RUN and wins a field both passes claim: it read the
	// whole crawl, so its value rests on more of the site.
	//
	// When the FIRST pass failed, this one is the lane's answer rather than a
	// refinement, so the read is whole: reporting it partial would warn a
	// user about nothing missing.
	return mergeProfileFields(current, fields), firstErr != nil
}

// mergeProfileFields folds a re-run's profile into the first pass's, keeping
// every field either of them grounded.
//
// Picking the LONGER of the two lists throws away work: the passes read
// different evidence, so the second can ground `usp` and `history` while
// missing an `industry` the first had, and a count comparison silently drops
// it. Both answers are gated the same way, so a field present in either is a
// field the site supports.
//
// `second` wins a contested field. Order is not meaningful to any reader of
// these fields — the legal gate and the name reader select by field name —
// so the result is `second` followed by the first pass's leftovers.
func mergeProfileFields(first, second []evidencedField) []evidencedField {
	if len(second) == 0 {
		return first
	}
	merged := append([]evidencedField(nil), second...)
	claimed := make(map[string]bool, len(second))
	for _, field := range second {
		claimed[field.Field] = true
	}
	for _, field := range first {
		if !claimed[field.Field] {
			merged = append(merged, field)
		}
	}
	return merged
}

// pageFactsCollector accumulates the streamed fact lane's outcomes. Pages
// complete from up to pageExtractConcurrency goroutines at once, so every
// fold goes through its lock; once every lane has been waited on the fields
// are read directly.
type pageFactsCollector struct {
	onDraft func(pageFactsResult)

	mu                    sync.Mutex
	results               []pageFactsResult
	published             pageFactsResult
	failed                []error
	legalCensusIncomplete bool
}

// record folds one page's outcome in: a failure joins the collected errors
// and costs only that page's findings, a success republishes the read so
// far. A failed LEGAL page also undercounts the entity census, which the
// legal gate must not trust.
func (c *pageFactsCollector) record(page crawlPage, res pageFactsResult, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err != nil {
		c.failed = append(c.failed, fmt.Errorf("page %s: %w", page.URL, err))
		if page.Kind == crmcontracts.SiteReadPageKindImpressum {
			c.legalCensusIncomplete = true
		}
		return
	}
	c.results = append(c.results, res)
	c.published = publishDraft(c.onDraft, c.results, c.published)
}

// publishDraft hands the caller the read SO FAR whenever it changed, so
// the SPA fills in findings while the remaining pages are still being
// read. Sorting by URL first keeps the fold stable as completions arrive
// in scheduler order. Returns what was published, for the next
// comparison. The caller holds the results lock.
func publishDraft(onDraft func(pageFactsResult), results []pageFactsResult, published pageFactsResult) pageFactsResult {
	if onDraft == nil {
		return published
	}
	snapshot := append([]pageFactsResult(nil), results...)
	sort.Slice(snapshot, func(i, j int) bool { return snapshot[i].url < snapshot[j].url })
	merged := mergePageResults(snapshot)
	if slices.Equal(merged.facts, published.facts) && slices.Equal(merged.people, published.people) &&
		slices.Equal(merged.entities, published.entities) {
		return published
	}
	onDraft(merged)
	return merged
}

// safeExtractPageFacts recovers a panic from extractPageFacts into an
// ordinary error. Both crawlAndExtract and extractSite run this lane
// from its own goroutine among up to pageExtractConcurrency siblings: an
// unrecovered panic in any one of them kills the whole process — this
// file's own "one page's failure costs that page's findings, never the
// whole fan-out" contract must hold even when the failure is a panic,
// not a returned error.
func safeExtractPageFacts(ctx context.Context, x evidenceExtractor, page crawlPage) (res pageFactsResult, err error) {
	defer func() {
		if p := recover(); p != nil {
			// recover() suppresses the runtime's own stack dump, so this is
			// the only place that trace still exists — capture it into the
			// internal log now, before it's gone; the returned error stays
			// a short, stack-free line since it can surface on a dossier's
			// warnings, not just internal diagnostics.
			slog.ErrorContext(ctx, "extraction panic recovered", "lane", "page_facts", "url", page.URL, "panic", p, "stack", string(debug.Stack()))
			err = fmt.Errorf("panic: %v", p)
		}
	}()
	return x.extractPageFacts(ctx, page)
}

// safeExtractProfile is safeExtractPageFacts' counterpart for the profile
// lane, which runs concurrently with the same page-fact fan-out.
func safeExtractProfile(ctx context.Context, x evidenceExtractor, pages []crawlPage) (fields []evidencedField, err error) {
	defer func() {
		if p := recover(); p != nil {
			slog.ErrorContext(ctx, "extraction panic recovered", "lane", "profile", "panic", p, "stack", string(debug.Stack()))
			err = fmt.Errorf("panic: %v", p)
		}
	}()
	return x.extractProfile(ctx, pages)
}

func snapshotCrawlPages(mu *sync.Mutex, pages *[]crawlPage) []crawlPage {
	mu.Lock()
	defer mu.Unlock()
	return append([]crawlPage(nil), (*pages)...)
}

// mergeInCommitOrder folds streamed per-page results deterministically:
// completion order is scheduler noise, so results re-order to the
// crawl's commit order before the fold.
func mergeInCommitOrder(crawl siteCrawl, results []pageFactsResult) pageFactsResult {
	byURL := map[string]pageFactsResult{}
	for _, res := range results {
		byURL[res.url] = res
	}
	ordered := make([]pageFactsResult, 0, len(results))
	for _, page := range crawl.Pages {
		if res, ok := byURL[page.URL]; ok {
			ordered = append(ordered, res)
		}
	}
	return mergePageResults(ordered)
}

// progressReporter serializes the progress callback for the fan-out
// spine: it fires from many goroutines at once, and locking here spares
// the caller its own lock.
func progressReporter(onPage func(done int)) func() {
	var done atomic.Int32
	var mu sync.Mutex
	return func() {
		n := int(done.Add(1))
		if onPage == nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		onPage(n)
	}
}

// extractSite runs the profile lane and the per-page fact lane in
// parallel over ALREADY-crawled pages — the non-streaming spine the
// unit tests drive directly; production overlaps via crawlAndExtract.
func extractSite(ctx context.Context, x evidenceExtractor, pages []crawlPage, onPage func(done int)) siteExtraction {
	var out siteExtraction

	results := make([]pageFactsResult, len(pages))
	errs := make([]error, len(pages))
	report := progressReporter(onPage)
	var wg sync.WaitGroup
	sem := make(chan struct{}, pageExtractConcurrency)
	for i, page := range pages {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			results[i], errs[i] = safeExtractPageFacts(ctx, x, page)
			report()
		}()
	}

	var profileErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		out.fields, profileErr = safeExtractProfile(ctx, x, pages)
	}()
	wg.Wait()

	var failed []error
	kept := make([]pageFactsResult, 0, len(results))
	for i, err := range errs {
		if err != nil {
			failed = append(failed, fmt.Errorf("page %s: %w", pages[i].URL, err))
			if pages[i].Kind == crmcontracts.SiteReadPageKindImpressum {
				out.legalCensusIncomplete = true
			}
			continue
		}
		kept = append(kept, results[i])
	}
	if profileErr != nil {
		failed = append(failed, fmt.Errorf("profile lane: %w", profileErr))
	}
	out.merged = mergePageResults(kept)
	out.err = errors.Join(failed...)
	return out
}

// pageKindsOf indexes the crawled pages' kinds by URL — what the legal
// gate needs to test a field's source page.
func pageKindsOf(pages []crawlPage) map[string]crmcontracts.SiteReadPageKind {
	kinds := make(map[string]crmcontracts.SiteReadPageKind, len(pages))
	for _, page := range pages {
		kinds[page.URL] = page.Kind
	}
	return kinds
}
