// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What the page-fact case refuses before a paid run spends anything on it: an
// expectation this page's own menu could never satisfy, a page that would issue
// no call at all, and a scenario shaped like something else entirely. They live
// beside the reply-grading tests rather than among them because a refusal is a
// claim about the CORPUS, and a verdict is a claim about a model.

import (
	"encoding/json"
	"strings"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/modules/people"
)

// An expectation the gate can never satisfy would measure nothing for as long as
// it stayed in the corpus. Prepare is where that gets named, while it is still a
// wiring error rather than a paid run of zeros.
func TestSitePageFactsCaseRefusesAnUnreachableExpectation(t *testing.T) {
	cases := []struct {
		name       string
		kind       crmcontracts.SiteReadPageKind
		text       string
		want       map[string]string
		wantReason string
	}{
		{
			name:       "a field this page kind's menu never offers",
			kind:       crmcontracts.SiteReadPageKindServices,
			text:       sitePageFactsText,
			want:       map[string]string{people.FactFoundedYear: "1998"},
			wantReason: "never offers",
		},
		{
			// A team page is called for its people and told its facts must be empty,
			// so a fact expectation over one could never be answered.
			name:       "any fact at all on a page whose menu carries none",
			kind:       crmcontracts.SiteReadPageKindTeam,
			text:       sitePageFactsText,
			want:       sitePageFactsWantAudit,
			wantReason: "a team page's menu never offers",
		},
		{
			name:       "an empty value, which the gate drops from every reply",
			kind:       crmcontracts.SiteReadPageKindServices,
			text:       sitePageFactsText,
			want:       map[string]string{people.FactService: "   "},
			wantReason: "empty value",
		},
		{
			// A site animates its headline numbers up from zero and the fetched DOM
			// carries the pre-animation figure, so the gate drops one — whichever
			// passage cites it.
			name:       "a measured zero, which the gate drops as a pre-animation figure",
			kind:       crmcontracts.SiteReadPageKindHome,
			text:       sitePageFactsText,
			want:       map[string]string{people.FactQuantifiedOutcome: "0 B + GMV enabled"},
			wantReason: "animated up from zero",
		},
		{
			name:       "a value whose name no passage of this page carries",
			kind:       crmcontracts.SiteReadPageKindServices,
			text:       sitePageFactsText,
			want:       map[string]string{people.FactService: "Phishing Simulation"},
			wantReason: "no passage of this fixture",
		},
		{
			name:       "no expectation at all",
			kind:       crmcontracts.SiteReadPageKindServices,
			text:       sitePageFactsText,
			want:       map[string]string{},
			wantReason: "expects no fact",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := sitePageFactsCases{}.Prepare(
				sitePageFactsFixtureJSON(t, tc.kind, tc.text), sitePageFactsJSON(t, tc.want))
			if err == nil {
				t.Fatalf("a scenario expecting %v prepared", tc.want)
			}
			if !strings.Contains(err.Error(), tc.wantReason) {
				t.Errorf("the refusal does not say why it is unreachable: %v", err)
			}
		})
	}
}

// A fixture the lane would never call a model for certifies a request the product
// never issues, whatever the reply to it looks like.
func TestSitePageFactsCaseRefusesAPageThatWouldIssueNoCall(t *testing.T) {
	cases := []struct {
		name       string
		kind       crmcontracts.SiteReadPageKind
		text       string
		wantReason string
	}{
		{
			// Boilerplate and unclassified pages state few facts and their calls would
			// dominate cost rather than quality, so the lane skips them entirely.
			name:       "a page kind the menu routes to no call",
			kind:       crmcontracts.SiteReadPageKindOther,
			text:       sitePageFactsText,
			wantReason: "never issues",
		},
		{
			name:       "a page whose text carries no passage",
			kind:       crmcontracts.SiteReadPageKindServices,
			text:       "   \n\n ",
			wantReason: "no passage",
		},
		{
			// The kind is not decoration: it selects the menu, which is half this
			// call's prompt and half its schema.
			name:       "a page kind the crawler never assigns",
			kind:       crmcontracts.SiteReadPageKind("careers"),
			text:       sitePageFactsText,
			wantReason: "careers",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := sitePageFactsCases{}.Prepare(
				sitePageFactsFixtureJSON(t, tc.kind, tc.text), sitePageFactsJSON(t, sitePageFactsWantAudit))
			if err == nil {
				t.Fatal("a page the fact lane would never call a model for prepared")
			}
			if !strings.Contains(err.Error(), tc.wantReason) {
				t.Errorf("the refusal does not say what the page lacks: %v", err)
			}
		})
	}
}

// A scenario shaped like something else asserts nothing about the reply, and a
// case that ran it anyway would report a number nobody wrote a claim for.
func TestSitePageFactsCaseRefusesAnExpectationItCannotRead(t *testing.T) {
	for _, expected := range []json.RawMessage{nil, json.RawMessage(`["service"]`), json.RawMessage(`7`)} {
		_, err := sitePageFactsCases{}.Prepare(sitePageFactsCatalogFixture(t), expected)
		if err == nil {
			t.Fatalf("a scenario expecting %s prepared", expected)
		}
		if !strings.Contains(err.Error(), "field to value") {
			t.Errorf("the refusal does not say what an expectation must be: %v", err)
		}
	}
}
