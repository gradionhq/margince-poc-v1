// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The deep read's extraction orchestration: the page-fact fan-out and
// the one profile call run CONCURRENTLY — their wall clock is the
// product's read time. Collect-don't-cancel: one page's failure costs
// that page's findings and degrades the read to partial, never the
// whole fan-out; the worker and the debug CLI share this exact spine.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
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

// profileTriggerPages is how many committed pages the profile lane
// waits for before firing: commits arrive in selection order, so the
// first dozen ARE the identity-dense excerpt set — waiting for the whole
// crawl would put the crawl's slow tail on the profile's critical path.
const profileTriggerPages = 12

// crawlAndExtract OVERLAPS the crawl and the extraction: page-fact
// calls launch the moment their page commits, and the profile call
// fires once the top-ranked pages are in (or the crawl ends, whichever
// is first). The read's wall clock becomes ~max(crawl, slowest lane)
// instead of their sum. onPage (may be nil) fires serially with the
// extracted-page count.
func crawlAndExtract(ctx context.Context, crawler *siteCrawler, x evidenceExtractor, seedURL string, onPage func(done int)) (siteCrawl, siteExtraction, error) {
	var out siteExtraction
	var results []pageFactsResult
	var failed []error
	var mu sync.Mutex
	report := progressReporter(onPage)

	var wg sync.WaitGroup
	sem := make(chan struct{}, pageExtractConcurrency)
	var committed []crawlPage
	var committedMu sync.Mutex

	profileOnce := sync.Once{}
	var profileWg sync.WaitGroup
	var profileErr error
	fireProfile := func() {
		profileOnce.Do(func() {
			committedMu.Lock()
			snapshot := make([]crawlPage, len(committed))
			copy(snapshot, committed)
			committedMu.Unlock()
			profileWg.Add(1)
			go func() {
				defer profileWg.Done()
				out.fields, profileErr = x.extractProfile(ctx, snapshot)
			}()
		})
	}

	crawlStart := time.Now()
	crawl, crawlErr := crawler.CrawlStream(ctx, seedURL, func(page crawlPage) {
		committedMu.Lock()
		committed = append(committed, page)
		count := len(committed)
		committedMu.Unlock()
		if count >= profileTriggerPages {
			fireProfile()
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			res, err := x.extractPageFacts(ctx, page)
			mu.Lock()
			if err != nil {
				failed = append(failed, fmt.Errorf("page %s: %w", page.URL, err))
				if page.Kind == crmcontracts.SiteReadPageKindImpressum {
					out.legalCensusIncomplete = true
				}
			} else {
				results = append(results, res)
			}
			mu.Unlock()
			report()
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
	fireProfile() // a small crawl may end below the trigger
	wg.Wait()
	profileWg.Wait()

	if profileErr != nil {
		failed = append(failed, fmt.Errorf("profile lane: %w", profileErr))
	}
	out.merged = mergeInCommitOrder(crawl, results)
	out.err = errors.Join(failed...)
	return crawl, out, nil
}

// progressReporter serializes the progress callback: it fires from the
// fan-out's goroutines, and locking here spares every caller (CLI
// printer, progress store) its own lock.
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
			results[i], errs[i] = x.extractPageFacts(ctx, page)
			report()
		}()
	}

	var profileErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		out.fields, profileErr = x.extractProfile(ctx, pages)
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
