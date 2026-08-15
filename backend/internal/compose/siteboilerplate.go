// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// Navigation is not evidence, and on a mega-menu site it is most of what the
// profile lane gets to read.
//
// Every page's excerpt is capped from its START (profileExcerptPages), and a
// large B2B site opens each page with the same header, mega-menu and language
// switcher. The budget is then spent before the page's own prose begins: on
// algolia.com the /about excerpt is still listing "Auto Parts, B2B Commerce,
// Ecommerce, Fashion, Grocery" at the cut, and the profile came back with two
// fields out of fifteen. Sites without that chrome — bestit.de, admetrics.io —
// return twelve to fifteen from the same budget.
//
// The fix needs no model call and no extra fetch, because the crawl already
// holds the whole corpus: text that appears near-verbatim at the head of most
// pages of one site is chrome BY DEFINITION, and can be measured. Nothing is
// inferred about what navigation looks like; the repetition is the evidence.
//
// Deliberately conservative. It only trims a COMMON PREFIX, only when several
// pages share it, and it always leaves the page's own text alone — a page that
// happens to be short, or a site with no shared header, passes through
// untouched. Over-trimming would destroy real evidence, and a missing fact is
// harder to notice than a noisy one.

import (
	"strings"
	"unicode/utf8"
)

const (
	// boilerplateMinPages is how many pages must agree before a shared
	// opening counts as chrome. Two pages can share an opening by accident
	// — a locale pair, or two pages of one template — and trimming on that
	// evidence would cut real text from a small site.
	boilerplateMinPages = 4
	// boilerplateMinShare is the fraction of pages that must carry the
	// prefix. A menu is on nearly every page; a section header is not.
	boilerplateMinShare = 0.6
	// boilerplateMinRunes is the shortest prefix worth removing. Below
	// this the win is noise and the risk of eating a real sentence is real.
	boilerplateMinRunes = 200
	// boilerplateMinRemainderRunes is how much a page must retain after the
	// prefix comes off to count as still carrying content. Below this the
	// page is a stub, and a prefix that stubs nearly every page is not a
	// header -- it is the pages being near-identical.
	boilerplateMinRemainderRunes = 120
	// boilerplateMaxShare bounds how much of a page the prefix may be. A
	// "shared prefix" covering ALL of a page means the pages are near
	// duplicates, not that they share a header, and cutting it would leave
	// nothing to read.
	//
	// Set on what SURVIVES rather than on how big the block is: algolia's
	// mega-menu is legitimately most of a page, and refusing to cut it is
	// refusing to fix the only case that needs fixing. What must not
	// happen is every page being reduced to a stub, which is what
	// near-identical pages produce.
	boilerplateMaxStubShare = 0.5
	// boilerplateMinSurvivingRunes is how much of a page must remain after
	// the shared run is cut for the cut to be worth making. Algolia keeps
	// ~5,800 runes; a page left with a few hundred was never a page with a
	// header, it was a page that repeats its neighbours.
	boilerplateMinSurvivingRunes = 400
)

// stripSharedPrefix removes the navigation chrome that opens most of a site's
// pages, so the profile excerpt spends its budget on the company's own words.
//
// Returns the pages in the same order, with Text trimmed where a shared
// opening was found. The input is not modified.
func stripSharedPrefix(pages []crawlPage) []crawlPage {
	out := make([]crawlPage, len(pages))
	copy(out, pages)
	if len(pages) < boilerplateMinPages {
		return out
	}

	prefix := sharedOpening(pages)
	if utf8.RuneCountInString(prefix) < boilerplateMinRunes {
		return out
	}

	for i, page := range out {
		// The block may sit behind the page's own title, so cut it out
		// wherever it starts rather than only at rune zero.
		at := strings.Index(page.Text, prefix)
		if at < 0 {
			continue
		}
		// Only a block at the TOP is chrome. The same text found deep in a
		// page is that page's own content -- near-identical pages share
		// long passages, and cutting one out of the middle mangles the
		// prose instead of removing a menu.
		if utf8.RuneCountInString(page.Text[:at]) > chromeSearchRunes {
			continue
		}
		trimmed := strings.TrimSpace(page.Text[:at] + " " + page.Text[at+len(prefix):])
		// Never hand back an empty page: a page that is ONLY chrome keeps
		// its text, so the reader still sees it exists rather than
		// silently losing a URL from the corpus.
		if trimmed == "" {
			continue
		}
		out[i].Text = trimmed
	}
	return out
}

// chromeSearchRunes is how far into a page the shared header may start. Real
// pages open with their own <title> ("About | Algolia", "Contact | Algolia")
// BEFORE the menu, so a strict prefix comparison finds nothing on exactly the
// sites this exists for. The menu still starts early; it just is not at rune
// zero.
const chromeSearchRunes = 300

// sharedOpening finds the longest opening that enough pages begin with,
// allowing each page a short unique lead-in first.
//
// It grows the candidate from the most common first-page opening rather than
// comparing every pair: the header is identical across pages by construction,
// so the pairwise common prefix of any two chrome-carrying pages already IS
// the header.
func sharedOpening(pages []crawlPage) string {
	best := ""
	// Try each page as the reference. A site whose FIRST page is atypical
	// (a landing page with no menu) would otherwise defeat the whole check.
	for _, candidate := range pages {
		if utf8.RuneCountInString(candidate.Text) < boilerplateMinRunes {
			continue
		}
		// Skip past this page's own lead-in and take the run that follows
		// as the chrome candidate. Anchoring on a mid-page offset is what
		// lets a menu be found under a per-page <title>.
		for _, start := range chromeStarts(candidate.Text) {
			block := blockFrom(candidate, start, pages)
			if utf8.RuneCountInString(block) > utf8.RuneCountInString(best) {
				best = block
			}
		}
	}
	return best
}

// blockFrom narrows one candidate opening against every other page and
// answers the shared run, or "" when the pages do not agree it is chrome.
func blockFrom(candidate crawlPage, start int, pages []crawlPage) string {
	common := candidate.Text[start:]
	agreeing := 0
	for _, other := range pages {
		if other.URL == candidate.URL {
			continue
		}
		shared := longestSharedRun(common, other.Text)
		if utf8.RuneCountInString(shared) < boilerplateMinRunes {
			continue
		}
		// Shrink to what this page also has, so the result is what the
		// AGREEING pages share rather than what one page happens to open with.
		common = shared
		agreeing++
	}
	if agreeing == 0 {
		return ""
	}
	if float64(agreeing+1)/float64(len(pages)) < boilerplateMinShare {
		return ""
	}
	if tooLargeAShare(common, pages) || blockDominatesPages(common, pages) {
		return ""
	}
	return common
}

// chromeStarts are the byte offsets worth testing as the head of the shared
// block: the very start, and each word boundary within the first
// chromeSearchRunes. Bounded on purpose — the menu is near the top or it is
// not chrome.
func chromeStarts(text string) []int {
	starts := []int{0}
	limit := len(text)
	if runes := []rune(text); len(runes) > chromeSearchRunes {
		limit = len(string(runes[:chromeSearchRunes]))
	}
	for i := 1; i < limit; i++ {
		if text[i-1] == ' ' && text[i] != ' ' {
			starts = append(starts, i)
		}
	}
	return starts
}

// longestSharedRun returns the longest run of `head` that also appears near
// the top of `other`, cut back to a word boundary.
func longestSharedRun(head, other string) string {
	limit := len(other)
	if runes := []rune(other); len(runes) > chromeSearchRunes {
		limit = len(string(runes[:chromeSearchRunes]))
	}
	for start := 0; start < limit; start++ {
		if start > 0 && (other[start-1] != ' ' || other[start] == ' ') {
			continue
		}
		if shared := commonPrefix(head, other[start:]); utf8.RuneCountInString(shared) >= boilerplateMinRunes {
			return shared
		}
	}
	return ""
}

// blockDominatesPages reports whether removing the shared run would leave a
// page that is mostly gone.
//
// A menu is a large slice of a page but not the whole of it: algolia's is 45%
// of its /about page, and 5,800 runes of that page survive. Pages that merely
// repeat one another share nearly everything, so what remains is a spliced
// fragment rather than prose worth extracting from.
func blockDominatesPages(block string, pages []crawlPage) bool {
	dominated, carrying := 0, 0
	for _, page := range pages {
		at := strings.Index(page.Text, block)
		if at < 0 {
			continue
		}
		carrying++
		remainder := strings.TrimSpace(page.Text[:at] + " " + page.Text[at+len(block):])
		if utf8.RuneCountInString(remainder) < boilerplateMinSurvivingRunes {
			dominated++
		}
	}
	if carrying == 0 {
		return false
	}
	return float64(dominated)/float64(carrying) > 0.5
}

// tooLargeAShare reports whether removing the prefix would leave the pages
// with nothing to distinguish them.
//
// Judged on what SURVIVES, not on the ratio: a mega-menu legitimately is most
// of a short page, and that is the case this whole file exists to fix. What is
// not legitimate is a "shared prefix" that leaves every page a stub, which is
// what near-duplicate pages produce.
func tooLargeAShare(prefix string, pages []crawlPage) bool {
	carrying, stubs := 0, 0
	for _, page := range pages {
		at := strings.Index(page.Text, prefix)
		if at < 0 {
			continue
		}
		carrying++
		remainder := strings.TrimSpace(page.Text[:at] + " " + page.Text[at+len(prefix):])
		if utf8.RuneCountInString(remainder) < boilerplateMinRemainderRunes {
			stubs++
		}
	}
	if carrying == 0 {
		return false
	}
	return float64(stubs)/float64(carrying) > boilerplateMaxStubShare
}

// commonPrefix returns the longest rune-aligned prefix shared by a and b,
// cut back to the last word boundary so a half-word is never left behind.
func commonPrefix(a, b string) string {
	limit := len(a)
	if len(b) < limit {
		limit = len(b)
	}
	end := 0
	for end < limit && a[end] == b[end] {
		end++
	}
	shared := a[:end]
	if !utf8.ValidString(shared) {
		// The cut landed inside a multi-byte rune; back up to the last
		// valid boundary.
		for len(shared) > 0 && !utf8.ValidString(shared) {
			shared = shared[:len(shared)-1]
		}
	}
	if space := strings.LastIndexAny(shared, " \t\n"); space > 0 {
		shared = shared[:space]
	}
	return shared
}
