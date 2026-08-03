// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"encoding/json"
	"testing"

	"github.com/gradionhq/margince/backend/internal/compose/aitasks"
)

func fixtureJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestSiteTriageCasePrepareRefusesAnUnreachableExpectation(t *testing.T) {
	page := fixtureJSON(t, siteTriageFixture{URL: "https://acme.example", Text: "We build robots."})

	// A scenario expecting a class the gate can never return would sit in the
	// corpus measuring nothing, and only a paid run would reveal it.
	if _, err := (siteTriageCases{}).Prepare(page, fixtureJSON(t, "probably-a-company")); err == nil {
		t.Error("Prepare accepted an expectation outside the closed vocabulary")
	}
	if _, err := (siteTriageCases{}).Prepare(fixtureJSON(t, "not a fixture"), fixtureJSON(t, siteKindCompany)); err == nil {
		t.Error("Prepare accepted a fixture that is not this site's shape")
	}
	for _, kind := range siteTriageKinds {
		if _, err := (siteTriageCases{}).Prepare(page, fixtureJSON(t, kind)); err != nil {
			t.Errorf("Prepare(%q) = %v, want the whole vocabulary accepted", kind, err)
		}
	}
}

func TestSiteTriageCaseEvaluateSeparatesARefusedReplyFromAWrongOne(t *testing.T) {
	page := fixtureJSON(t, siteTriageFixture{URL: "https://acme.example", Text: "We build robots."})
	prepared, err := (siteTriageCases{}).Prepare(page, fixtureJSON(t, siteKindCompany))
	if err != nil {
		t.Fatal(err)
	}

	cases := map[string]struct {
		output string
		want   string
	}{
		"the expected class": {`{"kind":"company","confidence":0.9,"reason":"sells robots"}`, aitasks.OutcomeAccepted},
		"a different class":  {`{"kind":"personal","confidence":0.9,"reason":"one person"}`, aitasks.OutcomeWrongAnswer},
		"unparseable":        {`not json`, aitasks.OutcomeInvalid},
		// The gate rewrites these to `unclear`. Reporting them as a WRONG
		// ANSWER would blame the model for a reply it never got to give, and a
		// paid run's report would read as disagreement rather than refusal.
		"a class that does not exist": {`{"kind":"maybe","confidence":0.9,"reason":"x"}`, aitasks.OutcomeInvalid},
		"confidence out of range":     {`{"kind":"company","confidence":9,"reason":"x"}`, aitasks.OutcomeInvalid},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := prepared.Evaluate(aitasks.Trace{Output: tc.output})
			if got.Result != tc.want {
				t.Errorf("Evaluate(%s) = %q (%s), want %q", tc.output, got.Result, got.Detail, tc.want)
			}
			if got.Result != aitasks.OutcomeAccepted && got.Detail == "" {
				t.Error("a non-accepted outcome carries no detail a paid run could be read from")
			}
		})
	}
}

func TestSiteTriageCaseSiteMatchesTheContract(t *testing.T) {
	site := (siteTriageCases{}).Site()
	if site.Variant != "triage" || string(site.Task) != "site_triage" {
		t.Errorf("Site() = %+v, want the site_triage/triage site the census registers", site)
	}
}
