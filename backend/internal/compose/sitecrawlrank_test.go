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
