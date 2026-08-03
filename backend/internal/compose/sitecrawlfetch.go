// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Reading ONE page: the paced fetch every crawl step goes through, and the
// landing-page read the domain triage does on its own before deciding whether
// the rest of the crawl is worth running. Reaching the seed at all — the
// www/scheme fallback ladder — is siteseed.go's job, and both callers go
// through it so neither invents its own idea of "unreachable".

import (
	"context"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/webread"
)

// ReadSeed fetches the landing page alone, under the crawler's own wall clock
// and pacing, and renders it as the crawl would. It buys the domain triage a
// verdict for one page instead of twelve.
//
// It goes through fetchSeed (siteseed.go), so the triage sees a site on the
// spelling that actually answers — a domain serving TLS on www but not on the
// apex is read, not written off as unreachable and refused a company on that.
// The answering URL is returned with the page for the same reason.
func (c *siteCrawler) ReadSeed(ctx context.Context, seedURL string) (crawlPage, error) {
	ctx, cancel := context.WithTimeout(ctx, c.wall)
	defer cancel()
	answered, page, err := c.fetchSeed(ctx, c.newPacer(), seedURL)
	if err != nil {
		return crawlPage{}, err
	}
	return crawlPage{
		URL: answered, Kind: crmcontracts.SiteReadPageKindHome,
		Text: page.Text, Bytes: page.Bytes,
	}, nil
}

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
