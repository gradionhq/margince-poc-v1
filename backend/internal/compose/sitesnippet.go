// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The reference-evidence engine: pages are segmented into numbered
// passages BEFORE extraction, the model cites a passage id instead of
// quoting text, and the gate resolves the id back into OUR text. The
// model writes ~3 tokens of evidence per finding instead of a sentence —
// the output-token reduction the ≤15s deep read stands on — and the
// stored evidence is provably the page's own words, because we never let
// the model produce them.

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// snippetMaxRunes bounds one passage: long enough to carry a claim and
	// its immediate context, short enough that citing it localizes the
	// evidence.
	snippetMaxRunes = 300
	// snippetMinRunes is the floor under which a passage is a fragment
	// (a heading, a nav crumb) and merges into its neighbor rather than
	// standing alone — a two-word passage would make containment gates
	// meaninglessly easy or meaninglessly hard.
	snippetMinRunes = 40
)

// segmentPassages splits stripped page text into passages: newline
// blocks first (the strongest block signal StripTags leaves), blocks
// over the cap split at sentence terminators, then a greedy forward
// merge up to the cap with undersized leftovers folded backward.
// Deterministic: same text, same passages, always.
func segmentPassages(text string) []string {
	var units []string
	for _, block := range strings.Split(text, "\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		if utf8.RuneCountInString(block) <= snippetMaxRunes {
			units = append(units, block)
			continue
		}
		units = append(units, splitSentences(block)...)
	}

	var passages []string
	var current strings.Builder
	currentRunes := 0
	flush := func() {
		if currentRunes == 0 {
			return
		}
		passages = append(passages, current.String())
		current.Reset()
		currentRunes = 0
	}
	for _, unit := range units {
		unitRunes := utf8.RuneCountInString(unit)
		if currentRunes > 0 && currentRunes+1+unitRunes > snippetMaxRunes {
			flush()
		}
		if currentRunes > 0 {
			current.WriteByte(' ')
			currentRunes++
		}
		current.WriteString(unit)
		currentRunes += unitRunes
	}
	flush()

	// Fold an undersized trailing passage back into its predecessor; the
	// forward merge already guarantees every other passage is either the
	// page's only one or was stopped by the cap.
	if n := len(passages); n > 1 && utf8.RuneCountInString(passages[n-1]) < snippetMinRunes {
		passages[n-2] = passages[n-2] + " " + passages[n-1]
		passages = passages[:n-1]
	}
	return passages
}

// splitSentences cuts one over-long block at sentence terminators
// (.!? followed by space and an upper-case letter or digit); a block
// with no such boundary is hard-cut at the rune cap so no unit can
// exceed it.
func splitSentences(block string) []string {
	var units []string
	runes := []rune(block)
	start := 0
	for i := 0; i < len(runes)-2; i++ {
		if (runes[i] == '.' || runes[i] == '!' || runes[i] == '?') &&
			unicode.IsSpace(runes[i+1]) &&
			(unicode.IsUpper(runes[i+2]) || unicode.IsDigit(runes[i+2])) {
			units = append(units, strings.TrimSpace(string(runes[start:i+1])))
			start = i + 2
		}
	}
	if start < len(runes) {
		units = append(units, strings.TrimSpace(string(runes[start:])))
	}
	// Hard-cut any sentence that alone exceeds the cap (minified text,
	// missing punctuation): the cap is a promise to the citation gate.
	var capped []string
	for _, unit := range units {
		r := []rune(unit)
		for len(r) > snippetMaxRunes {
			capped = append(capped, string(r[:snippetMaxRunes]))
			r = r[snippetMaxRunes:]
		}
		if len(r) > 0 {
			capped = append(capped, string(r))
		}
	}
	return capped
}

// snippetRef is one numbered passage's identity: which page it came
// from and its index there. The profile lane numbers passages globally
// across pages, so the RESOLVER — never the model — decides source_url.
type snippetRef struct {
	pageURL string
	passage string
	norm    string // normalizeEvidence(passage), computed once
}

// snippetIndex is one call's numbered evidence space: ids s0..sN in
// render order.
type snippetIndex struct {
	refs []snippetRef
}

// newSnippetIndex numbers the pages' passages in page order. Ids are
// index-derived ("s12"), so rendering and resolution can never disagree.
func newSnippetIndex(pages []crawlPage) snippetIndex {
	var idx snippetIndex
	for _, page := range pages {
		for _, passage := range segmentPassages(page.Text) {
			idx.refs = append(idx.refs, snippetRef{
				pageURL: page.URL,
				passage: passage,
				norm:    normalizeEvidence(passage),
			})
		}
	}
	return idx
}

// ids lists every citable id, in order — the schema enum that makes an
// uncitable id impossible at generation.
func (x snippetIndex) ids() []string {
	out := make([]string, len(x.refs))
	for i := range x.refs {
		out[i] = fmt.Sprintf("s%d", i)
	}
	return out
}

// renderNumbered lays the passages out for the model, grouped by page
// with OUR header lines (outside the untrusted spans, so page content
// cannot forge a boundary) and each passage tagged with its id.
func (x snippetIndex) renderNumbered() string {
	var b strings.Builder
	lastPage := ""
	for i, ref := range x.refs {
		if ref.pageURL != lastPage {
			if lastPage != "" {
				b.WriteString("</untrusted>\n")
			}
			fmt.Fprintf(&b, "\n=== PAGE %s ===\n<untrusted>\n", ref.pageURL)
			lastPage = ref.pageURL
		}
		fmt.Fprintf(&b, "[s%d] %s\n", i, ref.passage)
	}
	if lastPage != "" {
		b.WriteString("</untrusted>\n")
	}
	return b.String()
}

// resolve returns the cited passage, or ok=false for an id outside the
// index (unreachable when the schema enum held, the honest gate when a
// provider ignored it).
func (x snippetIndex) resolve(id string) (snippetRef, bool) {
	var n int
	if _, err := fmt.Sscanf(id, "s%d", &n); err != nil || n < 0 || n >= len(x.refs) {
		return snippetRef{}, false
	}
	return x.refs[n], true
}

// nameInCited answers whether name is normalized-contained in the cited
// passage, forgiving a one-passage boundary miss WITHIN the same page:
// item names often sit in a heading passage adjacent to the description
// the model cites, and an off-by-one citation is the same physical spot.
// The returned evidence is the passage (or same-page adjacent join) that
// actually carries the name — always our own text.
func (x snippetIndex) nameInCited(id, name string) (string, bool) {
	ref, ok := x.resolve(id)
	if !ok {
		return "", false
	}
	nameNorm := normalizeEvidence(name)
	if nameNorm == "" {
		return "", false
	}
	if strings.Contains(ref.norm, nameNorm) {
		return ref.passage, true
	}
	var n int
	if _, err := fmt.Sscanf(id, "s%d", &n); err != nil {
		return "", false
	}
	for _, adjacent := range []int{n - 1, n + 1} {
		if adjacent < 0 || adjacent >= len(x.refs) || x.refs[adjacent].pageURL != ref.pageURL {
			continue
		}
		if strings.Contains(x.refs[adjacent].norm, nameNorm) {
			if adjacent < n {
				return x.refs[adjacent].passage + " " + ref.passage, true
			}
			return ref.passage + " " + x.refs[adjacent].passage, true
		}
	}
	return "", false
}

// contentWordOverlap answers whether value and the cited passage share
// at least one content word (≥4 runes, normalized) — the warning-only
// plausibility signal for paraphrase fields, deliberately not a hard
// gate: a German page paraphrased into an English value legitimately
// shares nothing lexically.
func contentWordOverlap(value, passageNorm string) bool {
	for _, word := range strings.Fields(normalizeEvidence(value)) {
		if utf8.RuneCountInString(word) >= 4 && strings.Contains(passageNorm, word) {
			return true
		}
	}
	return false
}
