// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// The mega-menu that opens every page of a large B2B site. Long enough to
// exceed boilerplateMinRunes, which is the point: this is what eats the
// profile lane's excerpt budget on a real site.
const megaMenu = "Products AI Search and Retrieval Overview Search Show users " +
	"what they are looking for with AI-driven results. Recommendations Use " +
	"behavioural cues to drive higher engagement. Personalization Show each " +
	"user what they need across their journey. Analytics All your insights " +
	"in one dashboard. Solutions Industries SEE ALL Auto Parts B2B Commerce " +
	"Ecommerce Fashion Grocery Higher Education Marketplaces Media Retail " +
	"Company Partners Support Login Logout Language English Deutsch "

// filler stands in for the paragraphs a real page carries under its menu.
// The fixtures need it: a page of two sentences is not the case this file
// exists for, and a survivor threshold tuned for real pages would reject it.
func filler(topic string) string {
	return strings.Repeat(" "+topic+" is described here in the detail a real "+
		"page carries, across several sentences of ordinary prose.", 6)
}

func pagesWithChrome(bodies ...string) []crawlPage {
	pages := make([]crawlPage, 0, len(bodies))
	for i, body := range bodies {
		pages = append(pages, crawlPage{
			URL:  "https://example.test/p" + string(rune('a'+i)),
			Text: megaMenu + body + filler(body),
		})
	}
	return pages
}

func TestSharedNavigationIsStrippedSoThePageOwnWordsSurvive(t *testing.T) {
	const about = "About us. We were founded in 2009 in Dortmund and build " +
		"commerce software for mid-market retailers across Europe."
	pages := pagesWithChrome(
		about,
		"Careers. We are hiring engineers in Berlin and Hamburg.",
		"Contact. Reach the team at the Dortmund office any weekday.",
		"Services. Consulting, implementation and long-term platform support.",
		"Products. The platform covers search, merchandising and analytics.",
	)

	stripped := stripSharedPrefix(pages)

	// The menu must be gone. A few words of it may survive: commonPrefix
	// stops at a word boundary rather than mid-token, so the tail between
	// the last shared space and the page's own text stays. What matters is
	// that the excerpt budget now buys the company's own prose.
	if before, after := len(pages[0].Text), len(stripped[0].Text); after >= before {
		t.Fatalf("chrome was not removed: %d chars became %d", before, after)
	}
	if removed := len(pages[0].Text) - len(stripped[0].Text); removed < len(megaMenu)/2 {
		t.Fatalf("only %d chars removed from a %d-char menu", removed, len(megaMenu))
	}
	for i, page := range stripped {
		if strings.Contains(page.Text, "Auto Parts B2B Commerce") {
			t.Errorf("page %d kept the mega-menu body: %.80q", i, page.Text)
		}
	}
	if !strings.Contains(stripped[0].Text, "founded in 2009 in Dortmund") {
		t.Errorf("the page's own sentence was lost: %.120q", stripped[0].Text)
	}
}

func TestAPageThatIsOnlyNavigationKeepsItsText(t *testing.T) {
	// A URL whose whole body is chrome must not become empty: an empty page
	// vanishes from the corpus, and losing a page is worse than a noisy one.
	pages := pagesWithChrome(
		"About us. We build commerce software for mid-market retailers.",
		"Careers. We are hiring engineers in Berlin and Hamburg.",
		"Contact. Reach the team at the Dortmund office any weekday.",
		"Products. The platform covers search, merchandising and analytics.",
	)
	// One page is nothing but the menu.
	pages = append(pages, crawlPage{URL: "https://example.test/chrome", Text: megaMenu})

	stripped := stripSharedPrefix(pages)

	if strings.TrimSpace(stripped[4].Text) == "" {
		t.Fatal("a chrome-only page was emptied; it must keep its text")
	}
}

func TestASiteWithoutSharedNavigationIsLeftAlone(t *testing.T) {
	pages := []crawlPage{
		{URL: "https://example.test/a", Text: "About us. Founded in Dortmund in 2009, we build commerce software."},
		{URL: "https://example.test/b", Text: "Careers. Engineering roles in Berlin, Hamburg and Munich are open."},
		{URL: "https://example.test/c", Text: "Contact. The office is on Adessoplatz and answers on weekdays."},
		{URL: "https://example.test/d", Text: "Services. Consulting, implementation and platform support for retail."},
	}

	stripped := stripSharedPrefix(pages)

	for i := range pages {
		if stripped[i].Text != pages[i].Text {
			t.Errorf("page %d was trimmed though nothing is shared:\n got %q\nwant %q",
				i, stripped[i].Text, pages[i].Text)
		}
	}
}

func TestTooFewPagesToJudgeLeavesThemUntouched(t *testing.T) {
	// Two pages can share an opening by coincidence -- a locale pair, or one
	// template used twice -- and trimming on that evidence cuts real text.
	pages := pagesWithChrome(
		"About us. We build commerce software for retailers.",
		"Careers. We are hiring engineers in Berlin.",
	)

	stripped := stripSharedPrefix(pages)

	for i := range pages {
		if stripped[i].Text != pages[i].Text {
			t.Errorf("page %d trimmed on only %d pages of evidence", i, len(pages))
		}
	}
}

func TestNearDuplicatePagesAreNotGutted(t *testing.T) {
	// When the "shared prefix" is most of every page, the pages are near
	// duplicates rather than pages sharing a header. Removing it would
	// leave nothing to extract from.
	body := strings.Repeat("The same paragraph on every page. ", 20)
	pages := []crawlPage{
		{URL: "https://example.test/a", Text: body + "A"},
		{URL: "https://example.test/b", Text: body + "B"},
		{URL: "https://example.test/c", Text: body + "C"},
		{URL: "https://example.test/d", Text: body + "D"},
		{URL: "https://example.test/e", Text: body + "E"},
	}

	stripped := stripSharedPrefix(pages)

	for i := range pages {
		if stripped[i].Text != pages[i].Text {
			t.Errorf("page %d was gutted: %.60q", i, stripped[i].Text)
		}
	}
}

func TestProfileExcerptSpendsItsBudgetOnRealTextNotChrome(t *testing.T) {
	// The end-to-end point of the change: with chrome removed, the excerpt
	// the model is shown contains the company's own words.
	const claim = "Founded in 2009 in Dortmund, we build commerce software."
	pages := pagesWithChrome(
		"About us. "+claim,
		"Careers. We are hiring engineers in Berlin and Hamburg.",
		"Contact. Reach the team at the Dortmund office any weekday.",
		"Services. Consulting, implementation and long-term support.",
		"Products. Search, merchandising and analytics in one platform.",
	)

	excerpts := profileExcerptPages(pages)

	var corpus strings.Builder
	for _, page := range excerpts {
		corpus.WriteString(page.Text)
	}
	if !strings.Contains(corpus.String(), claim) {
		t.Fatalf("the excerpt lost the company's own sentence; got %.200q",
			corpus.String())
	}
}

func TestAHostileCorpusCannotBurnAWorkerGoroutine(t *testing.T) {
	// The deep read accepts an attacker-chosen URL (POST /company/site-reads
	// creates an unbound dossier), so the crawled site chooses this input.
	// Before the comparison was bounded, forty pages that all opened alike
	// cost minutes of CPU per read -- enough to occupy the workers by
	// repeating the call.
	pages := make([]crawlPage, 0, 40)
	opening := strings.Repeat("a ", 200_000)
	for i := 0; i < 40; i++ {
		pages = append(pages, crawlPage{
			URL:  fmt.Sprintf("https://hostile.test/p%d", i),
			Text: opening + fmt.Sprintf("%d", i),
		})
	}

	start := time.Now()
	stripSharedPrefix(pages)

	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("a hostile corpus took %v; the comparison is unbounded again", elapsed)
	}
}

func TestTheCutNeverInventsAPassageThePageDidNotContain(t *testing.T) {
	// The surviving text becomes the evidence quote a human is shown when
	// approving a staged proposal, and the citation gate checks containment
	// against it. Joining the pre-chrome lead-in to the post-chrome body
	// would form a sentence that appears nowhere on the page.
	pages := pagesWithChrome(
		"Our competitor Globex is the market leader in this segment.",
		"Careers. We are hiring engineers in Berlin and Hamburg.",
		"Contact. Reach the team at the Dortmund office any weekday.",
		"Services. Consulting, implementation and long-term support.",
	)
	for i := range pages {
		pages[i].Text = fmt.Sprintf("Page %d title ", i) + pages[i].Text
	}

	for i, page := range stripSharedPrefix(pages) {
		if !strings.Contains(pages[i].Text, page.Text) {
			t.Errorf("page %d now carries text the source never had:\n%.140q",
				i, page.Text)
		}
	}
}

func TestEachLanguageLosesItsOwnMenu(t *testing.T) {
	// A multilingual site carries one menu per language, and no single menu
	// is on a majority of pages: arvato.com crawls in four languages, so its
	// largest covers 10 of 38. One pass strips one menu and leaves the other
	// locales carrying theirs, so the search has to repeat.
	const englishMenu = "Products Solutions Industries Resources Company " +
		"Partners Support Login Logout Language English Deutsch Nederlands " +
		"Automotive Consumer Products Healthcare Retail Technology Careers " +
		"Investors Press Contact About us Why we exist Customers "
	const germanMenu = "Produkte Lösungen Branchen Ressourcen Unternehmen " +
		"Partner Support Anmelden Abmelden Sprache English Deutsch Nederlands " +
		"Automobil Konsumgüter Gesundheit Handel Technologie Karriere " +
		"Investoren Presse Kontakt Über uns Warum es uns gibt Kunden "

	var pages []crawlPage
	for i, menu := range []string{englishMenu, germanMenu} {
		for j := 0; j < 4; j++ {
			body := fmt.Sprintf("Page %d-%d. ", i, j) +
				filler(fmt.Sprintf("topic %d-%d", i, j))
			pages = append(pages, crawlPage{
				URL:  fmt.Sprintf("https://example.test/l%d/p%d", i, j),
				Text: fmt.Sprintf("Title %d-%d ", i, j) + menu + body,
			})
		}
	}

	stripped := stripSharedPrefix(pages)

	for i, page := range stripped {
		if strings.Contains(page.Text, "Investors Press Contact") ||
			strings.Contains(page.Text, "Investoren Presse Kontakt") {
			t.Errorf("page %d kept its menu: %.90q", i, page.Text)
		}
		if !strings.Contains(page.Text, "topic") {
			t.Errorf("page %d lost its own text: %.90q", i, page.Text)
		}
	}
}
