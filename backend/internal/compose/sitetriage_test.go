// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"strings"
	"testing"
)

func TestTriageVerdictStopsTheCrawlOnlyWhenItIsSureAndNegative(t *testing.T) {
	cases := []struct {
		name    string
		verdict siteTriageVerdict
		aborts  bool
	}{
		{"a confident personal page stops the crawl", siteTriageVerdict{Kind: siteKindPersonal, Confidence: 0.95}, true},
		{"a confident mailbox vendor stops the crawl", siteTriageVerdict{Kind: siteKindProvider, Confidence: 0.9}, true},
		{"a confident parked domain stops the crawl", siteTriageVerdict{Kind: siteKindParked, Confidence: 0.85}, true},
		// A company answer has nothing to stop: the crawl is exactly what
		// produces the dossier the organization is then named from.
		{"a company reads on", siteTriageVerdict{Kind: siteKindCompany, Confidence: 1}, false},
		{"unclear reads on", siteTriageVerdict{Kind: siteKindUnclear, Confidence: 1}, false},
		// Below the floor the deterministic evidence gets its say. Aborting on
		// a shaky refusal costs a real customer their company record.
		{"an unsure personal page reads on", siteTriageVerdict{Kind: siteKindPersonal, Confidence: 0.79}, false},
		{"an unsure vendor reads on", siteTriageVerdict{Kind: siteKindProvider, Confidence: 0.5}, false},
		{"a verdict with no confidence at all reads on", siteTriageVerdict{Kind: siteKindPersonal}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.verdict.Aborts(); got != tc.aborts {
				t.Errorf("Aborts() = %v, want %v", got, tc.aborts)
			}
		})
	}
}

func TestGateTriageVerdictFallsThroughRatherThanTrustingABadReply(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{"unparseable", "not json at all"},
		{"a kind that does not exist", `{"kind":"probably-a-company","confidence":0.99,"reason":"x"}`},
		{"confidence above one", `{"kind":"personal","confidence":4,"reason":"x"}`},
		{"negative confidence", `{"kind":"personal","confidence":-1,"reason":"x"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := gateTriageVerdict(tc.text)
			if got.Kind != siteKindUnclear {
				t.Errorf("gateTriageVerdict(%q).Kind = %q, want %q", tc.text, got.Kind, siteKindUnclear)
			}
			// The safe direction: an unreadable reply must never be the reason
			// a real company loses its record.
			if got.Aborts() {
				t.Errorf("gateTriageVerdict(%q) would stop a crawl on a reply it could not read", tc.text)
			}
			if got.Reason == "" {
				t.Errorf("gateTriageVerdict(%q) gives no reason a human could act on", tc.text)
			}
		})
	}
}

func TestGateTriageVerdictKeepsAWellFormedAnswer(t *testing.T) {
	got := gateTriageVerdict(`{"kind":"provider","confidence":0.93,"reason":"  sells mailboxes  "}`)
	if got.Kind != siteKindProvider || got.Confidence != 0.93 {
		t.Fatalf("gateTriageVerdict = %+v, want provider at 0.93", got)
	}
	if got.Reason != "sells mailboxes" {
		t.Errorf("reason = %q, want it trimmed", got.Reason)
	}
	if !got.Aborts() {
		t.Error("a confident vendor verdict must stop the crawl")
	}
}

func TestTriageRequestFencesThePageAndBoundsIt(t *testing.T) {
	// A rune that appears nowhere else in the request, so the count below
	// measures the page text and not the url or the fence nonce.
	long := strings.Repeat("Ω", triageExcerptRunes*2)
	req := triageRequest(crawlPage{URL: "https://acme.example", Text: long})

	if len(req.Messages) != 1 {
		t.Fatalf("%d messages, want the page alone", len(req.Messages))
	}
	body := req.Messages[0].Content
	// The page text is a crawled site's own writing: it must arrive inside the
	// call's own nonce boundary, never loose in the prompt.
	if strings.Contains(req.System, long) || !strings.Contains(body, "https://acme.example") {
		t.Error("the page must ride in the fenced user message, not the system prompt")
	}
	if carried := strings.Count(body, "Ω"); carried > triageExcerptRunes {
		t.Errorf("the excerpt carries %d runes of page text, want at most %d", carried, triageExcerptRunes)
	}
	if req.ResponseSchema == nil {
		t.Error("the classification call must pin its response schema")
	}
	if req.SecretStripper == nil {
		t.Error("the classification call must strip secrets like every other model call")
	}
}

func TestTriageStatusMapsOntoTheLedgersVocabulary(t *testing.T) {
	// The classifier has five words and the ledger has three. Parked and
	// unclear both mean "nothing identified anybody", which is what no_site
	// records — a fourth word for the same fact would only give operators two
	// things to learn.
	cases := map[string]string{
		siteKindPersonal: "personal",
		siteKindProvider: "provider",
		siteKindParked:   "no_site",
		siteKindUnclear:  "no_site",
	}
	for kind, want := range cases {
		if got := triageStatusFor(kind); got != want {
			t.Errorf("triageStatusFor(%q) = %q, want %q", kind, got, want)
		}
	}
}

func TestTriageDomainOfRecoversTheDomainFromItsSeed(t *testing.T) {
	cases := map[string]string{
		"https://kestner.example":    "kestner.example",
		"https://mail.acme.co.uk":    "acme.co.uk",
		"https://rowanmarsh.example": "rowanmarsh.example",
		"":                           "",
	}
	for seed, want := range cases {
		if got := triageDomainOf(seed); got != want {
			t.Errorf("triageDomainOf(%q) = %q, want %q", seed, got, want)
		}
	}
}
