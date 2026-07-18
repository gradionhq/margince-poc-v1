// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The corpus builder's contract: identity-dense pages pack first,
// boilerplate last, page texts are capped, chunks split on page
// boundaries under the budget, and the rendering keeps our page headers
// outside the untrusted spans.

import (
	"strings"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

func corpusFixturePage(url string, kind crmcontracts.SiteReadPageKind, text string) crawlPage {
	return crawlPage{URL: url, Kind: kind, Text: text}
}

func TestBuildCorpusChunksPacksByFactDensityAndSplitsOnBudget(t *testing.T) {
	pages := []crawlPage{
		corpusFixturePage(seedURL+"/blog/post", crmcontracts.SiteReadPageKindOther, strings.Repeat("b", 50)),
		corpusFixturePage(seedURL, crmcontracts.SiteReadPageKindHome, strings.Repeat("h", 50)),
		corpusFixturePage(seedURL+"/services", crmcontracts.SiteReadPageKindServices, strings.Repeat("s", 50)),
		corpusFixturePage(seedURL+"/impressum", crmcontracts.SiteReadPageKindImpressum, strings.Repeat("i", 50)),
	}
	chunks := buildCorpusChunks(pages, 120)
	if len(chunks) != 2 {
		t.Fatalf("packed into %d chunks, want 2 under a 120-rune budget", len(chunks))
	}
	// Rank order: impressum (70) > home (ranked with contact, 50) >
	// services (45) > boilerplate blog (1); the 120-rune budget fits two.
	first := []string{chunks[0].pages[0].URL, chunks[0].pages[1].URL}
	if first[0] != seedURL+"/impressum" || first[1] != seedURL {
		t.Fatalf("chunk one must carry the identity spine first, got %v", first)
	}
	last := chunks[len(chunks)-1].pages
	if last[len(last)-1].URL != seedURL+"/blog/post" {
		t.Fatalf("boilerplate must pack last, got %v", last)
	}
	total := 0
	for _, chunk := range chunks {
		total += len(chunk.pages)
	}
	if total != len(pages) {
		t.Fatalf("packing lost pages: %d of %d", total, len(pages))
	}
}

func TestBuildCorpusChunksCapsPageTextAndTheChunkCount(t *testing.T) {
	oversized := corpusFixturePage(seedURL, crmcontracts.SiteReadPageKindHome, strings.Repeat("x", maxExtractionText+500))
	chunks := buildCorpusChunks([]crawlPage{oversized}, corpusBudgetRunes)
	if len(chunks) != 1 || len([]rune(chunks[0].pages[0].Text)) != maxExtractionText {
		t.Fatalf("page text not capped at %d runes", maxExtractionText)
	}

	var many []crawlPage
	for i := 0; i < maxCorpusChunks+2; i++ {
		many = append(many, corpusFixturePage(seedURL+"/p"+strings.Repeat("x", i+1), crmcontracts.SiteReadPageKindOther, strings.Repeat("y", 100)))
	}
	capped := buildCorpusChunks(many, 100) // one page per chunk
	if len(capped) != maxCorpusChunks {
		t.Fatalf("chunk count %d, want the %d cap", len(capped), maxCorpusChunks)
	}
}

func TestRenderCorpusKeepsHeadersOutsideUntrustedSpans(t *testing.T) {
	chunk := corpusChunk{pages: []crawlPage{
		corpusFixturePage(seedURL+"/about", crmcontracts.SiteReadPageKindAbout, "We are Acme."),
	}}
	prompt, pageURLs := renderCorpus(seedURL, chunk)
	if len(pageURLs) != 1 || pageURLs[0] != seedURL+"/about" {
		t.Fatalf("pageURLs = %v", pageURLs)
	}
	header := "=== PAGE " + seedURL + "/about (about) ==="
	headerAt := strings.Index(prompt, header)
	untrustedAt := strings.Index(prompt, "<untrusted>")
	if headerAt == -1 || untrustedAt == -1 || headerAt > untrustedAt {
		t.Fatalf("the page header must precede the untrusted span:\n%s", prompt)
	}
	if !strings.Contains(prompt, "<untrusted>We are Acme.</untrusted>") {
		t.Fatalf("page text must sit inside its own untrusted span:\n%s", prompt)
	}
}
