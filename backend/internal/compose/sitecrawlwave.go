// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The crawl's wave machinery, split from the walk itself (sitecrawl.go):
// serial admission screening, the concurrent wave fetch, and the
// serial in-order commit that keeps the walk deterministic whatever
// order the responses arrive in.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/webread"
)

// admission is one candidate that passed the serial pre-fetch screen:
// its fetchable URL and classified kind.
type admission struct {
	cand crawlCandidate
	url  string
	kind crmcontracts.SiteReadPageKind
}

// admit screens one selected candidate before its wave fetches: URL
// identity, dedupe, probe-kind preference, locale collapse (legal pages
// exempt — the multi-entity conflict guard can only count entities on
// the legal pages it actually reads), XML indexes, and the same-site
// gate. Runs single-threaded — it mutates the dedupe state.
func (r *crawlRun) admit(cand crawlCandidate) (admission, bool) {
	candURL, ok := normalizeCandidate(cand.url)
	if !ok || r.visited[candURL] {
		return admission{}, false
	}
	r.visited[candURL] = true
	if cand.probe && r.probeKindDone[cand.kind] {
		// A probe of an already-satisfied kind: skipped without a report
		// entry — the path was our guess, not a page the site offered.
		return admission{}, false
	}
	kind := cand.kind
	if kind == "" {
		kind = classifyKind(candURL)
	}
	if !r.legalCensusOpen(kind) && r.canonicalDone[localeCanonical(candURL)] {
		// A locale variant of a page already read (/de/about after
		// /about): a translation restates the same document, and exact-
		// text dedupe cannot see that. One language per document.
		return admission{}, false
	}
	if strings.HasSuffix(strings.ToLower(candURL), ".xml") {
		// A sitemapindex's <loc>s are child sitemaps; the crawl deliberately
		// does not recurse into them (bounded discovery), and an XML file is
		// not a readable page either way.
		return admission{}, false
	}
	if !webread.SameRegistrableDomain(r.seedURL, candURL) {
		// The security property of the whole crawler: no page content can
		// send the crawl off the seed's site — off-domain candidates are
		// recorded, never fetched.
		r.skip(candURL, crmcontracts.SiteReadSkipReasonOffDomain)
		return admission{}, false
	}
	return admission{cand: cand, url: candURL, kind: kind}, true
}

// fetchResult is one wave fetch's outcome, committed in wave order.
type fetchResult struct {
	page webread.Page
	dur  time.Duration
	err  error
}

// fetchWave fetches the admitted candidates concurrently through the
// pacer. Only the fetch runs in parallel; everything that mutates crawl
// state stays with commit.
func (r *crawlRun) fetchWave(ctx context.Context, admitted []admission) []fetchResult {
	results := make([]fetchResult, len(admitted))
	var wg sync.WaitGroup
	for i, adm := range admitted {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := time.Now()
			page, err := r.crawler.fetchPaced(ctx, r.pacer, adm.url)
			results[i] = fetchResult{page: page, dur: time.Since(start), err: err}
		}()
	}
	wg.Wait()
	return results
}

// commit files one fetched result — a page, a recorded skip, a silent
// legalCensusOpen reports whether an Impressum candidate may still bypass
// the one-language-per-document rule. The bypass exists because a group's
// per-locale legal pages can name DIFFERENT legal entities, and the entity
// census must see them all. But it is a budget hole: a six-language site
// offers six translations of one legal notice, each at the highest kind
// priority, and unbounded they buy six slots the offering pages then never
// get. Two is enough to notice a second entity; past that a translation is
// a translation.
const maxLegalLocalePages = 2

func (r *crawlRun) legalCensusOpen(kind crmcontracts.SiteReadPageKind) bool {
	return kind == crmcontracts.SiteReadPageKindImpressum && r.impressumRead < maxLegalLocalePages
}

// commit folds one fetched candidate into the run — as a page, a
// skip, or the crawl's stop — in wave order, single-threaded. The probe-
// kind and locale rechecks exist because a wave admits its candidates
// BEFORE any of them fetched: two probes of one kind (or two locales of
// one document) can share a wave, and the second must fold once the
// first landed.
func (r *crawlRun) commit(adm admission, res fetchResult) {
	if adm.cand.probe && r.probeKindDone[adm.cand.kind] {
		// An earlier commit in this wave satisfied the kind: the guess is
		// moot, whatever its fetch returned — same silence admit gives a
		// moot probe pre-fetch.
		return
	}
	if !r.legalCensusOpen(adm.kind) && r.canonicalDone[localeCanonical(adm.url)] {
		// Same for a locale variant whose document landed earlier in the
		// wave.
		return
	}
	switch {
	case errors.Is(res.err, webread.ErrRobotsDisallowed):
		r.skip(adm.url, crmcontracts.SiteReadSkipReasonRobots)
		return
	case errors.Is(res.err, context.DeadlineExceeded) || errors.Is(res.err, context.Canceled):
		// The crawl's clock ran out mid-fetch; stopping here avoids a bogus
		// per-page "unreadable".
		r.crawl.Stopped = stoppedPtr(crmcontracts.SiteReadReportStoppedReasonDeadline)
		return
	case res.err != nil:
		r.skip(adm.url, crmcontracts.SiteReadSkipReasonUnreadable)
		return
	}
	page := res.page
	if utf8.RuneCountInString(page.Text) < crawlMinRunes {
		r.skip(adm.url, crmcontracts.SiteReadSkipReasonUnreadable)
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
		r.skip(adm.url, crmcontracts.SiteReadSkipReasonByteCap)
		r.crawl.Stopped = stoppedPtr(crmcontracts.SiteReadReportStoppedReasonByteCap)
		return
	}

	r.seenText[page.Text] = true
	r.canonicalDone[localeCanonical(adm.url)] = true
	if adm.kind == crmcontracts.SiteReadPageKindImpressum {
		r.impressumRead++
	}
	r.totalBytes += page.Bytes
	if adm.cand.probe {
		r.probeKindDone[adm.cand.kind] = true
	}
	committed := crawlPage{URL: adm.url, Kind: adm.kind, Text: page.Text, Bytes: page.Bytes, FetchDur: res.dur}
	r.crawl.Pages = append(r.crawl.Pages, committed)
	if r.onPage != nil {
		r.onPage(committed)
	}
	r.queue = append(r.queue, linkCandidates(page.Links)...)
}
