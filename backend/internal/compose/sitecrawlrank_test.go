// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"strings"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

func TestClassifyKindSeparatesLegalIdentityFromLegalProductsAndPolicies(t *testing.T) {
	for _, rawURL := range []string{
		"https://example.com/imprint",
		"https://example.com/de/publisher",
		"https://example.com/de/legal",
		"https://example.com/c/legal",
		"https://example.com/legal/miro-imprint",
	} {
		if got := classifyKind(rawURL); got != crmcontracts.SiteReadPageKindImpressum {
			t.Errorf("classifyKind(%q) = %q, want impressum", rawURL, got)
		}
	}
	for _, rawURL := range []string{
		"https://example.com/teams/legal",
		"https://example.com/legal/privacy-at-example",
		"https://example.com/legal/terms-of-service",
	} {
		if got := classifyKind(rawURL); got == crmcontracts.SiteReadPageKindImpressum {
			t.Errorf("classifyKind(%q) = impressum; a product or policy page must not consume legal-identity budget", rawURL)
		}
	}
}

func TestClassifyKindDoesNotPromoteGuidesBecauseTheirSlugsMentionTeamsOrProducts(t *testing.T) {
	for _, rawURL := range []string{
		"https://example.com/guides/how-teams-use-comments",
		"https://example.com/help/guides/building-a-product-requirement-document",
		"https://example.com/product/ai/use-cases/chat-about-anything",
	} {
		got := classifyKind(rawURL)
		if strings.Contains(rawURL, "/product/") {
			if got != crmcontracts.SiteReadPageKindProducts {
				t.Errorf("classifyKind(%q) = %q, want products from the leading path family", rawURL, got)
			}
			continue
		}
		if got != crmcontracts.SiteReadPageKindOther {
			t.Errorf("classifyKind(%q) = %q, want other", rawURL, got)
		}
	}
}

// Every URL here is a real path from the demo corpus, and every one of them
// classified `other` before the Vietnamese and Korean words were added — so the
// reader never looked for staff on any of them. vinatechgroup.vn is the case
// that shows what it cost: it names its chairman on /gioi-thieu, the only page
// that DID classify was the English /en/about-us, and that page names nobody,
// so the company read as publishing no staff at all.
func TestClassifyKindReadsVietnameseAndKoreanPageNames(t *testing.T) {
	for rawURL, want := range map[string]crmcontracts.SiteReadPageKind{
		"https://vinatechgroup.vn/gioi-thieu":                    crmcontracts.SiteReadPageKindAbout,
		"https://thinksmart.com.vn/gioi-thieu":                   crmcontracts.SiteReadPageKindAbout,
		"https://tinthanhphat.vn/gioi-thieu/gioi-thieu-cong-ty/": crmcontracts.SiteReadPageKindAbout,
		// The one qualifier a real about page carries is the site's OWN name,
		// matched against the host rather than guessed at as grammar.
		"https://tth-automation.com/gioi-thieu-ve-tth-automation.html": crmcontracts.SiteReadPageKindAbout,
		"https://hanmyviet.vn/gioi-thieu-han-my-viet.html":             crmcontracts.SiteReadPageKindAbout,
		"https://tth-automation.com/lien-he.html":                      crmcontracts.SiteReadPageKindContact,
		"https://itgtechnology.vn/lien-he/":                            crmcontracts.SiteReadPageKindContact,
		"https://aubot.vn/vi/lien-he/":                                 crmcontracts.SiteReadPageKindContact,
		// Korean sites percent-encode Hangul in links; url.Parse decodes it
		// into Path, so both spellings have to land in the same place.
		"https://example.co.kr/회사소개":                                 crmcontracts.SiteReadPageKindAbout,
		"https://example.co.kr/%ED%9A%8C%EC%82%AC%EC%86%8C%EA%B0%9C": crmcontracts.SiteReadPageKindAbout,
		"https://example.co.kr/임직원":                                  crmcontracts.SiteReadPageKindTeam,
		"https://example.co.kr/연락처":                                  crmcontracts.SiteReadPageKindContact,
	} {
		if got := classifyKind(rawURL); got != want {
			t.Errorf("classifyKind(%q) = %q, want %q", rawURL, got, want)
		}
	}
}

// The widened vocabulary must not start naming ordinary pages, and every URL
// here is one the FIRST version of it got wrong — matched as a substring
// anywhere in the leading segment rather than as a prefix of it.
//
// They are in the first path segment on purpose: classifyKind only ever looks
// at that one, so a case built from a two-segment path passes without
// exercising the match at all. That was the bug in this test before it was the
// bug in the code.
//
// A false `about` costs more than a missed one: the profile lane spends a
// limited page budget on what classifyKind names, so a wrong classification
// starves the commercial evidence rather than merely failing to help.
func TestClassifyKindStillRefusesOrdinaryPagesInEveryLanguage(t *testing.T) {
	for _, rawURL := range []string{
		// "introducing our new PRODUCT", not an about page.
		"https://example.vn/gioi-thieu-san-pham-moi",
		// A press release. `introduce` is an English verb before it is a page.
		"https://example.com/acme-introduces-widget",
		// Staff BENEFITS, not a staff directory.
		"https://example.co.kr/임직원복지",
		// A new-year greeting, not the chairman's introduction.
		"https://example.co.kr/신년인사말",
		// A product name that happens to contain "lienhe".
		"https://example.com/alienhero",
		// Ordinary section words, deliberately never in the lists.
		"https://example.com/company",
		"https://example.com/info",
		"https://example.co.kr/news",
	} {
		if got := classifyKind(rawURL); got != crmcontracts.SiteReadPageKindOther {
			t.Errorf("classifyKind(%q) = %q, want other", rawURL, got)
		}
	}
}

func TestProfileEvidenceReadyRequiresCommercialPages(t *testing.T) {
	pages := make([]crawlPage, profileTriggerNonLegalPages+12)
	for i := range pages {
		pages[i].Kind = crmcontracts.SiteReadPageKindImpressum
	}
	if profileEvidenceReady(pages) {
		t.Fatal("legal pages alone must not fire the one-shot profile lane")
	}
	for i := 0; i < profileTriggerNonLegalPages; i++ {
		pages[i].Kind = crmcontracts.SiteReadPageKindAbout
	}
	if !profileEvidenceReady(pages) {
		t.Fatal("the commercial evidence threshold must fire the profile lane")
	}
}

func TestUntakenCandidatesIncludesLinksDiscoveredByAStoppingCommit(t *testing.T) {
	queue := []crawlCandidate{{url: seedURL + "/selected"}, {url: seedURL + "/discovered-at-cap"}}
	got := untakenCandidates(queue, []bool{true})
	if len(got) != 1 || got[0].url != seedURL+"/discovered-at-cap" {
		t.Fatalf("new queue entries without a taken slot are untaken, got %+v", got)
	}
}

func TestAnIndustryPageNamedPublisherIsNotAnImprint(t *testing.T) {
	// "publisher" names an imprint at the top of a site and an ordinary
	// industry everywhere else. arvato.com publishes /industries/publisher
	// in four languages; reading those as legal pages counted six of its 38
	// pages as legal, which starves the commercial evidence the profile is
	// built from and votes in the census that withholds the legal fields.
	legal := []string{
		"https://example.test/publisher",
		"https://example.test/de/publisher",
		"https://example.test/impressum",
	}
	for _, rawURL := range legal {
		if !legalIdentityPath(rawURL) {
			t.Errorf("%s should be read as the site's imprint", rawURL)
		}
	}

	notLegal := []string{
		"https://example.test/industries/publisher",
		"https://example.test/de/branchen/publisher",
		"https://example.test/nl/branches/publisher",
		"https://example.test/pt/setores/publisher",
		"https://example.test/solutions/publisher",
	}
	for _, rawURL := range notLegal {
		if legalIdentityPath(rawURL) {
			t.Errorf("%s is an industry page, not the site's imprint", rawURL)
		}
	}
}

// TestLegalIdentityPathKnowsOtherLanguagesNotMarketingPages widens the legal
// classifier to the mandatory legal-notice page as other countries name it,
// and pins the line it must not cross.
//
// The demo dataset had 60 companies where no page was classified as a legal
// notice, and not one of them yielded a registered address. Most publish
// only /contact and /about — those stay OUT, because an address taken off a
// marketing page is the guess the legal gate exists to refuse.
func TestLegalIdentityPathKnowsOtherLanguagesNotMarketingPages(t *testing.T) {
	for _, url := range []string{
		"https://example.com/mentions-legales",
		"https://example.com/aviso-legal",
		"https://example.com/note-legali",
		"https://example.com/legal-notice",
		"https://example.com/fr/mentions-legales",
	} {
		if !legalIdentityPath(url) {
			t.Errorf("%s is a mandatory legal notice and was not treated as one", url)
		}
	}
	for _, url := range []string{
		"https://example.com/contact",
		"https://example.com/contact-us",
		"https://example.com/about",
		"https://example.com/about-us",
		"https://example.com/company",
		"https://example.com/privacy-policy",
		"https://example.com/terms",
	} {
		if legalIdentityPath(url) {
			t.Errorf("%s is not a legal notice, and treating it as one lets an address come off a marketing page", url)
		}
	}
}
