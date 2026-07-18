// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The snippet engine's contract: deterministic segmentation under the
// rune caps, index-derived ids that render and resolve identically, and
// a containment check that forgives a heading/description boundary but
// never a different page or an absent name.

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

func TestSegmentPassagesIsDeterministicAndCapped(t *testing.T) {
	text := "Cloud Cost Audit\nA line-by-line review of cloud spend identifying waste across compute, storage and networking. " +
		strings.Repeat("More substantive prose about the audit follows here. ", 12) +
		"\nKurz." // an undersized trailing fragment that must fold backward
	first := segmentPassages(text)
	second := segmentPassages(text)
	if !reflect.DeepEqual(first, second) {
		t.Fatal("segmentation diverged between runs")
	}
	if len(first) < 2 {
		t.Fatalf("expected multiple passages, got %d", len(first))
	}
	for i, passage := range first {
		if n := utf8.RuneCountInString(passage); n > snippetMaxRunes+snippetMinRunes {
			t.Fatalf("passage %d is %d runes — the cap is broken", i, n)
		}
	}
	last := first[len(first)-1]
	if utf8.RuneCountInString(last) < snippetMinRunes {
		t.Fatalf("an undersized fragment survived as its own passage: %q", last)
	}
	if !strings.Contains(strings.Join(first, " "), "Kurz.") {
		t.Fatal("the folded fragment's text was lost")
	}
}

func TestSegmentPassagesHardCutsUnpunctuatedText(t *testing.T) {
	blob := strings.Repeat("x", 3*snippetMaxRunes)
	for i, passage := range segmentPassages(blob) {
		if utf8.RuneCountInString(passage) > snippetMaxRunes {
			t.Fatalf("passage %d exceeds the cap on unpunctuated text", i)
		}
	}
}

func snippetFixtureIndex() snippetIndex {
	return newSnippetIndex([]crawlPage{
		{
			URL: seedURL + "/services", Kind: crmcontracts.SiteReadPageKindServices,
			Text: "Cloud Cost Audit\nA line-by-line review of cloud spend identifying waste across compute, storage, networking and observability tooling.",
		},
		{
			URL: seedURL + "/about", Kind: crmcontracts.SiteReadPageKindAbout,
			Text: "Wir sind Acme Robotics — Automatisierung seit 1998, mit Werken in Stuttgart und Hanoi fuer industrielle Kunden.",
		},
	})
}

func TestSnippetIndexRendersAndResolvesTheSameIds(t *testing.T) {
	idx := snippetFixtureIndex()
	rendered := idx.renderNumbered()
	for _, id := range idx.ids() {
		ref, ok := idx.resolve(id)
		if !ok {
			t.Fatalf("id %s from ids() does not resolve", id)
		}
		if !strings.Contains(rendered, "["+id+"] ") {
			t.Fatalf("id %s missing from the rendering", id)
		}
		if !strings.Contains(rendered, ref.passage) {
			t.Fatalf("passage of %s missing from the rendering", id)
		}
	}
	if _, ok := idx.resolve("s99"); ok {
		t.Fatal("an out-of-range id resolved")
	}
	if _, ok := idx.resolve("12"); ok {
		t.Fatal("a malformed id resolved")
	}
	// Page headers stay outside the untrusted spans.
	if strings.Index(rendered, "=== PAGE "+seedURL+"/services ===") > strings.Index(rendered, "Cloud Cost Audit") {
		t.Fatal("the page header must precede its content")
	}
}

func TestNameInCitedForgivesTheHeadingBoundaryNeverThePage(t *testing.T) {
	// A services page whose item name is its own heading block, with the
	// description long enough to be a separate passage.
	idx := newSnippetIndex([]crawlPage{
		{URL: seedURL + "/services", Text: "Cloud Cost Audit\n" +
			strings.Repeat("A thorough line-by-line review of every cloud bill position follows. ", 6)},
		{URL: seedURL + "/other", Text: strings.Repeat("Entirely different content on another page. ", 6)},
	})
	if len(idx.refs) < 3 {
		t.Fatalf("fixture needs the heading and description in separate passages, got %d", len(idx.refs))
	}
	// Citing the DESCRIPTION passage still evidences the name via the
	// adjacent heading passage on the same page.
	evidence, ok := idx.nameInCited("s1", "Cloud Cost Audit")
	if !ok {
		t.Fatal("the adjacent-heading join must recover the boundary miss")
	}
	if !strings.Contains(evidence, "Cloud Cost Audit") {
		t.Fatalf("returned evidence must carry the name: %q", evidence)
	}
	// A passage on ANOTHER page never evidences it, however close its index.
	lastID := idx.ids()[len(idx.ids())-1]
	if _, ok := idx.nameInCited(lastID, "Cloud Cost Audit"); ok {
		t.Fatal("a different page's passage evidenced the name")
	}
	// An absent name is never evidenced.
	if _, ok := idx.nameInCited("s0", "Nonexistent Service"); ok {
		t.Fatal("an absent name was evidenced")
	}
}

func TestContentWordOverlapIsAWarningSignalNotAGate(t *testing.T) {
	passage := normalizeEvidence("Wir liefern Automatisierung für die Industrie seit 1998.")
	if !contentWordOverlap("Industrial Automatisierung provider", passage) {
		t.Fatal("a shared content word must count as overlap")
	}
	if contentWordOverlap("Digital consultancy for retail", passage) {
		t.Fatal("no shared content words means no overlap")
	}
}
