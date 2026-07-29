// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package orgbrief

// The deterministic floor's branches. It is the brief every deployment gets,
// so what it says — and refuses to say — is worth pinning line by line.

import (
	"strings"
	"testing"
)

func briefLines(sentences []Sentence) string {
	var out strings.Builder
	for _, sentence := range sentences {
		out.WriteString(sentence.Text)
		out.WriteString(" ")
	}
	return out.String()
}

func TestDeterministicOpensWithWhatTheAccountIs(t *testing.T) {
	text := briefLines(Deterministic(briefOrgID, Input{
		Name: "Brandt Automotive GmbH", Industry: "Automotive", SizeBand: "201-500",
	}))
	for _, want := range []string{"Brandt Automotive GmbH", "Automotive", "201-500"} {
		if !strings.Contains(text, want) {
			t.Errorf("the identity line omits %q: %q", want, text)
		}
	}
	// No contacts, so no score: a strength figure with no contact behind it
	// is a number the reader cannot act on.
	if strings.Contains(text, "Relationship strength") {
		t.Errorf("reported a strength for an account with no known contact: %q", text)
	}
}

// A field the account does not carry is absent, never guessed — the same
// evidence-or-omit rule the record page follows.
func TestDeterministicOmitsWhatTheAccountDoesNotCarry(t *testing.T) {
	text := briefLines(Deterministic(briefOrgID, Input{Name: "Acme"}))
	if strings.TrimSpace(text) != "Acme." {
		t.Errorf("an account with only a name produced %q", text)
	}
}

func TestDeterministicNamesEveryStalledDeal(t *testing.T) {
	in := Input{
		Name: "Acme",
		OpenDeals: []DealIn{
			{ID: "d-1", Name: "Fleet retrofit", AmountMinor: 100_000, Currency: "EUR", Stalled: true},
			{ID: "d-2", Name: "Depot pilot", AmountMinor: 50_000, Currency: "EUR"},
			{ID: "d-3", Name: "Spare parts", AmountMinor: 25_000, Currency: "EUR", Stalled: true},
		},
	}
	sentences := Deterministic(briefOrgID, in)
	text := briefLines(sentences)
	for _, want := range []string{"Fleet retrofit", "Spare parts"} {
		if !strings.Contains(text, want) {
			t.Errorf("the stalled line omits %q: %q", want, text)
		}
	}
	if strings.Contains(text, "Depot pilot") {
		t.Errorf("a deal that is not stalled was named as stalled: %q", text)
	}
	// The stalled sentence cites exactly the stalled deals, so the card can
	// link the two the rep has to act on and not the one they need not.
	var stalled []Evidence
	for _, sentence := range sentences {
		if strings.Contains(sentence.Text, "Stalled") {
			stalled = sentence.Evidence
		}
	}
	if len(stalled) != 2 {
		t.Fatalf("the stalled sentence cites %d deals, want the two that stalled", len(stalled))
	}
	for _, cited := range stalled {
		if cited.EntityID == "d-2" {
			t.Error("the stalled sentence cites the deal that is not stalled")
		}
	}
}

// A pipeline sentence reports the account's money and cites every open deal,
// so the reader can open any of them from it.
func TestDeterministicPipelineCitesEveryOpenDeal(t *testing.T) {
	sentences := Deterministic(briefOrgID, Input{
		Name: "Acme",
		OpenDeals: []DealIn{
			{ID: "d-1", Name: "A", AmountMinor: 400_000, Currency: "EUR"},
			{ID: "d-2", Name: "B", AmountMinor: 100_000, Currency: "EUR"},
		},
		WonLifetime: 1_200_000,
		LostCount:   3,
	})
	var pipeline *Sentence
	for i := range sentences {
		if strings.Contains(sentences[i].Text, "open deal") {
			pipeline = &sentences[i]
		}
	}
	if pipeline == nil {
		t.Fatalf("no pipeline sentence: %q", briefLines(sentences))
	}
	if !strings.Contains(pipeline.Text, "5000 EUR") {
		t.Errorf("the pipeline total is wrong: %q", pipeline.Text)
	}
	if !strings.Contains(pipeline.Text, "12000 EUR won") {
		t.Errorf("the lifetime won figure is missing: %q", pipeline.Text)
	}
	if len(pipeline.Evidence) != 2 {
		t.Errorf("the pipeline sentence cites %d deals, want both", len(pipeline.Evidence))
	}
}

// An activity with no subject still reports the contact — the kind and the
// date are the facts; inventing a subject would not be.
func TestDeterministicLastTouchSurvivesAMissingSubject(t *testing.T) {
	withSubject := briefLines(Deterministic(briefOrgID, Input{
		Name:   "Acme",
		Recent: []ActIn{{ID: "a-1", Kind: "call", Subject: "Pricing", At: "2026-07-10T09:00:00Z"}},
	}))
	if !strings.Contains(withSubject, `"Pricing"`) {
		t.Errorf("the subject is not quoted as theirs: %q", withSubject)
	}

	without := briefLines(Deterministic(briefOrgID, Input{
		Name:   "Acme",
		Recent: []ActIn{{ID: "a-1", Kind: "call", At: "2026-07-10T09:00:00Z"}},
	}))
	if !strings.Contains(without, "call") {
		t.Errorf("a subjectless activity lost its kind: %q", without)
	}
	if strings.Contains(without, `""`) {
		t.Errorf("a subjectless activity rendered an empty quote: %q", without)
	}
}

func TestDeterministicReportsOpenTasks(t *testing.T) {
	text := briefLines(Deterministic(briefOrgID, Input{
		Name: "Acme",
		OpenTasks: []NamedIn{
			{ID: "t-1", Name: "Send the paperwork"},
			{ID: "t-2", Name: "Book the walkthrough"},
		},
	}))
	if !strings.Contains(text, "2 open task") {
		t.Errorf("the task count is missing: %q", text)
	}
	if !strings.Contains(text, "Send the paperwork") {
		t.Errorf("the first task is not named: %q", text)
	}
}

// Minor units from different currencies do not add up to money in either of
// them, and labelling the sum with whichever deal came first states it as a
// fact. A mixed-currency account gets the count and no total.
func TestDeterministicRefusesToTotalAcrossCurrencies(t *testing.T) {
	text := briefLines(Deterministic(briefOrgID, Input{
		Name: "Acme",
		OpenDeals: []DealIn{
			{ID: "d-1", Name: "EU deal", AmountMinor: 400_000, Currency: "EUR"},
			{ID: "d-2", Name: "US deal", AmountMinor: 100_000, Currency: "USD"},
		},
	}))
	if !strings.Contains(text, "2 open deal") {
		t.Errorf("the deal count is missing: %q", text)
	}
	if strings.Contains(text, "worth about") {
		t.Errorf("summed across currencies: %q", text)
	}
	for _, currency := range []string{"EUR", "USD"} {
		if strings.Contains(text, currency) {
			t.Errorf("named %s on a mixed-currency account: %q", currency, text)
		}
	}
}

// An amountless deal contributes no money and no currency, so it never turns
// a single-currency account into a mixed one.
func TestDeterministicTotalsPastAnAmountlessDeal(t *testing.T) {
	text := briefLines(Deterministic(briefOrgID, Input{
		Name: "Acme",
		OpenDeals: []DealIn{
			{ID: "d-1", Name: "Priced", AmountMinor: 400_000, Currency: "EUR"},
			{ID: "d-2", Name: "Not priced yet"},
		},
	}))
	if !strings.Contains(text, "4000 EUR") {
		t.Errorf("the priced deal's total is missing: %q", text)
	}
}
