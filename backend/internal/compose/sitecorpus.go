// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The deep read's corpus builder: the whole crawled site folded into the
// labeled text ONE model call extracts from. Pages are ordered by fact
// density (legal and about pages first, boilerplate archives last), each
// capped, and greedy-packed along page boundaries into at most
// maxCorpusChunks chunks — one chunk is the normal case (a 40-page site
// measures ~80k tokens), the chunk fallback covers outsized sites
// without an unbounded call.

import (
	"fmt"
	"sort"
	"strings"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

const (
	// corpusBudgetRunes bounds one corpus call's page text. 240k runes
	// (~80k tokens) keeps the measured gradion.com corpus — the founder's
	// manually-validated one-call case — in a single call.
	corpusBudgetRunes = 240_000
	// maxCorpusChunks caps the chunk fallback so the job timeout's
	// arithmetic is a real bound; the tail beyond it is boilerplate by
	// construction of the ordering.
	maxCorpusChunks = 4
)

// corpusChunk is one call's worth of pages, in corpus priority order,
// texts already capped to what the model will actually see.
type corpusChunk struct {
	pages []crawlPage
}

// corpusRank orders pages for packing: the identity-dense kinds first —
// same ranking the crawler's candidate priority uses, boilerplate last —
// so chunk one always carries the legal/about/team spine and truncation
// only ever eats archives.
func corpusRank(page crawlPage) int {
	if boilerplatePath(page.URL) {
		return priBoilerplate
	}
	if pri, ok := kindPriority[page.Kind]; ok {
		return pri
	}
	if page.Kind == crmcontracts.SiteReadPageKindHome {
		// The landing page states positioning; rank it with the fact
		// kinds, not the generic tail.
		return kindPriority[crmcontracts.SiteReadPageKindContact]
	}
	return priOther
}

// buildCorpusChunks packs the crawled pages into extraction chunks:
// stable-sort by corpusRank (crawl order breaks ties), cap each page's
// text at maxExtractionText, greedy-pack along page boundaries up to
// budget runes per chunk, at most maxCorpusChunks chunks. Deterministic:
// the crawl is, the sort is stable, and the packing is greedy.
func buildCorpusChunks(pages []crawlPage, budget int) []corpusChunk {
	ordered := make([]crawlPage, len(pages))
	copy(ordered, pages)
	sort.SliceStable(ordered, func(i, j int) bool {
		return corpusRank(ordered[i]) > corpusRank(ordered[j])
	})

	var chunks []corpusChunk
	var current corpusChunk
	used := 0
	for _, page := range ordered {
		if runes := []rune(page.Text); len(runes) > maxExtractionText {
			page.Text = string(runes[:maxExtractionText])
		}
		size := len([]rune(page.Text))
		if used > 0 && used+size > budget {
			chunks = append(chunks, current)
			if len(chunks) == maxCorpusChunks {
				return chunks
			}
			current = corpusChunk{}
			used = 0
		}
		current.pages = append(current.pages, page)
		used += size
	}
	if len(current.pages) > 0 {
		chunks = append(chunks, current)
	}
	return chunks
}

// renderCorpus lays one chunk out for the model: per-page sections with
// the page's URL and kind in OUR header line, the page text inside its
// own <untrusted> wrapper. Headers stay outside the untrusted spans so
// page content cannot forge a section boundary and launder its claims
// onto another page's URL; the system prompt states that rule too.
func renderCorpus(seedURL string, chunk corpusChunk) (string, []string) {
	var b strings.Builder
	pageURLs := make([]string, 0, len(chunk.pages))
	fmt.Fprintf(&b, "Corpus of %d pages crawled from %s:\n", len(chunk.pages), seedURL)
	for _, page := range chunk.pages {
		pageURLs = append(pageURLs, page.URL)
		fmt.Fprintf(&b, "\n=== PAGE %s (%s) ===\n<untrusted>%s</untrusted>\n", page.URL, page.Kind, page.Text)
	}
	return b.String(), pageURLs
}
