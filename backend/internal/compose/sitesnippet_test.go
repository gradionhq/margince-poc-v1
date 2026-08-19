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
	"github.com/gradionhq/margince/backend/internal/shared/kernel/promptfence"
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
	rendered := idx.renderNumbered(promptfence.New())
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
	// The only thing outside the spans is the page ORDINAL — the URL is the
	// site's own text and goes inside, so a crafted path cannot be read as
	// part of the prompt.
	if strings.Index(rendered, "=== PAGE 1 ===") > strings.Index(rendered, "Cloud Cost Audit") {
		t.Fatal("the page header must precede its content")
	}
	frame, _, _ := strings.Cut(rendered, "url: ")
	if strings.Contains(frame, seedURL) {
		t.Fatalf("a crawl URL reached the prompt frame: %q", frame)
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

// TestNameInCitedAcceptsAnAddressSplitByMarkup pins the case that cost the
// demo dataset 94 company addresses.
//
// An Impressum prints the street, the postcode and city, and the country as
// separate elements. A faithful reading joins them into one value, and no
// contiguous run of the page ever equals that join — so the substring gate
// refused real addresses that were demonstrably on the page.
func TestNameInCitedAcceptsAnAddressSplitByMarkup(t *testing.T) {
	idx := newSnippetIndex([]crawlPage{{
		URL: seedURL + "/impressum",
		Text: "Impressum\nadesso SE\nAdessoplatz 1\n44269 Dortmund\nDeutschland\n" +
			strings.Repeat("Weitere Angaben nach Paragraf 5 TMG folgen an dieser Stelle. ", 4),
	}})
	for _, value := range []string{
		"Adessoplatz 1 44269 Dortmund",
		"Adessoplatz 1 44269 Dortmund Deutschland",
		"adesso SE",
	} {
		if _, ok := idx.nameInCited("s0", value); !ok {
			t.Errorf("the page carries %q across separate lines, and the gate refused it", value)
		}
	}
}

// TestNameInCitedStillRefusesWhatIsNotOnThePage is the other half of the
// relaxation above: forgiving markup must not forgive invention. Every case
// here has to keep failing, or the no-guess property is gone.
func TestNameInCitedStillRefusesWhatIsNotOnThePage(t *testing.T) {
	idx := newSnippetIndex([]crawlPage{{
		URL: seedURL + "/impressum",
		Text: "Impressum\nadesso SE\nAdessoplatz 1\n44269 Dortmund\nDeutschland\n" +
			strings.Repeat("Weitere Angaben nach Paragraf 5 TMG folgen an dieser Stelle. ", 4),
	}})
	for name, value := range map[string]string{
		"an invented street":     "Hauptstrasse 7 44269 Dortmund",
		"an invented city":       "Adessoplatz 1 44269 Bielefeld",
		"a wholly absent value":  "Rue de la Paix 4 75002 Paris",
		"the words out of order": "Dortmund 44269 Adessoplatz 1",
		"a fragment of a word":   "essoplat 1",
		"a single absent token":  "Bielefeld",
	} {
		if _, ok := idx.nameInCited("s0", value); ok {
			t.Errorf("%s was evidenced: %q", name, value)
		}
	}
}

// TestNameInCitedRefusesTokensAssembledAcrossAGap is the hole the legal
// census documents and this fallback must not open: a passage printing
// "24114 Kiel" and "HRB 123456" separately must never vouch for the
// invented "HRB 24114". Contiguity of content tokens is what forbids it.
func TestNameInCitedRefusesTokensAssembledAcrossAGap(t *testing.T) {
	idx := newSnippetIndex([]crawlPage{{
		URL: seedURL + "/impressum",
		Text: "Sitz der Gesellschaft: 24114 Kiel\nRegistergericht Kiel HRB 123456\n" +
			strings.Repeat("Weitere Pflichtangaben folgen hier. ", 5),
	}})
	if _, ok := idx.nameInCited("s0", "HRB 24114"); ok {
		t.Error("an identifier assembled from tokens printed apart was evidenced")
	}
	// The real, contiguous one still passes.
	if _, ok := idx.nameInCited("s0", "HRB 123456"); !ok {
		t.Error("the printed registration number was refused")
	}
}

// TestRealDroppedAddressesFromTheDemoCorpus replays actual values the demo
// dataset lost to value_not_in_snippet, against an Impressum laid out the way
// the real pages lay them out: one element per line.
func TestRealDroppedAddressesFromTheDemoCorpus(t *testing.T) {
	cases := []struct {
		page  string
		value string
	}{
		{
			page:  "Impressum\ncommunicode AG\nWittekindstr. 1a\n45131 Essen /NRW\nDeutschland\n",
			value: "Wittekindstr. 1a 45131 Essen /NRW Deutschland",
		},
		{
			page:  "Impressum\nFACT-Finder\nHabermehlstr. 17\n75172 Pforzheim\nGermany\n",
			value: "Habermehlstr. 17 75172 Pforzheim Germany",
		},
		{
			page:  "Kontakt\nFIS GmbH\nRöthleiner Weg 1\nD-97506 Grafenrheinfeld\n",
			value: "Röthleiner Weg 1 D-97506 Grafenrheinfeld",
		},
		{
			page:  "Impressum\nLaudert GmbH + Co. KG\nVon-Braun-Straße 8,\n48691 Vreden\n",
			value: "Von-Braun-Straße 8, 48691 Vreden",
		},
		{
			page:  "Aviso legal\nDoofinder S.L.\nRufino González 23 bis, 1º,\nMadrid 28037\n",
			value: "Rufino González 23 bis, 1º, Madrid 28037",
		},
	}
	for _, c := range cases {
		idx := newSnippetIndex([]crawlPage{{
			URL:  "https://example.com/impressum",
			Text: c.page + strings.Repeat("Weitere Pflichtangaben folgen an dieser Stelle. ", 4),
		}})
		if _, ok := idx.nameInCited("s0", c.value); !ok {
			t.Errorf("still dropped: %q", c.value)
		}
	}
}

// TestNameInCitedRefusesAValueWithNoWords closes a hole the token fallback
// opened: a punctuation-only value has no content tokens, and an empty claim
// is contained in everything, so "---" was grounded by any page at all.
// Nothing is not evidence.
func TestNameInCitedRefusesAValueWithNoWords(t *testing.T) {
	idx := newSnippetIndex([]crawlPage{{
		URL:  seedURL + "/impressum",
		Text: "Impressum\nadesso SE\nAdessoplatz 1\n44269 Dortmund\n" + strings.Repeat("Weitere Angaben folgen. ", 6),
	}})
	for _, junk := range []string{"---", "...", "///", "-", "•", "  "} {
		if _, ok := idx.nameInCited("s0", junk); ok {
			t.Errorf("a value with no words was evidenced: %q", junk)
		}
	}
}

// TestNameInCitedKeepsTheSeparatorsThePagePrinted is the other half of the
// token relaxation. Dropping punctuation is right for an address whose parts
// markup split apart, and wrong for an identifier: a page printing
// "HRB 123/456" must not ground "HRB 123-456", which is a different
// registration number from the one the page carries.
func TestNameInCitedKeepsTheSeparatorsThePagePrinted(t *testing.T) {
	idx := newSnippetIndex([]crawlPage{{
		URL:  seedURL + "/impressum",
		Text: "Impressum\nRegistergericht Kiel HRB 123/456\nAdessoplatz 1\n44269 Dortmund\n" + strings.Repeat("Weitere Angaben folgen. ", 6),
	}})
	if _, ok := idx.nameInCited("s0", "HRB 123-456"); ok {
		t.Error("a reformatted registration number was evidenced")
	}
	if _, ok := idx.nameInCited("s0", "HRB 123/456"); !ok {
		t.Error("the registration number as printed was refused")
	}
	// The address still passes: its parts are separated by spaces, which is
	// layout, not something the page said.
	if _, ok := idx.nameInCited("s0", "Adessoplatz 1 44269 Dortmund"); !ok {
		t.Error("the address was refused")
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
