// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package orgbrief

// What the model is actually shown about money and about tasks.
//
// Both defects these cover were invisible to every existing test, because every
// existing test asserted the PROSE the brief produces and this is about the
// JSON it is produced from. A number the model misreads and a task state the
// model has to guess both look like model failures from the outside — the
// account brief said a 180,000 EUR deal was worth eighteen million, and said
// open tasks had been completed, on a page whose own cards said otherwise.

import (
	"encoding/json"
	"strings"
	"testing"
)

// The prompt carries a figure a reader can read, in the currency's own scale.
func TestTheDealAmountReachesTheModelAsAMajorUnitFigure(t *testing.T) {
	for _, tc := range []struct {
		name     string
		minor    int64
		currency string
		want     string
		reject   string
	}{
		// The issue's own case: 180,000.00 EUR read as eighteen million.
		{"a two-decimal currency", 18_000_000, "EUR", `"amount":"180000.00"`, "18000000"},
		// The other direction, and the one `/100` gets wrong: yen has no minor
		// unit, so the integer IS the figure and dividing would understate it
		// a hundredfold.
		{"a zero-decimal currency", 18_000_000, "JPY", `"amount":"18000000"`, "180000.00"},
		// Three digits, the standard's other exception.
		{"a three-decimal currency", 18_000_000, "KWD", `"amount":"18000.000"`, "180000.00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := json.Marshal(DealIn{
				ID: "d-1", Name: "Fleet retrofit", AmountMinor: tc.minor, Currency: tc.currency,
			})
			if err != nil {
				t.Fatalf("encoding the deal: %v", err)
			}
			got := string(encoded)
			if !strings.Contains(got, tc.want) {
				t.Errorf("the prompt carries %s, want %s — the model reads this figure as the "+
					"amount and writes it into customer-facing prose", got, tc.want)
			}
			if strings.Contains(got, tc.reject) {
				t.Errorf("the prompt still carries %q in %s, which is the same money at the "+
					"wrong scale", tc.reject, got)
			}
		})
	}
}

// An amount with no currency has no scale, so it is not shown at all: a figure
// printed without its code is a number whose scale the reader has to guess,
// which is the defect rather than a lesser form of it.
func TestAnAmountWithNoCurrencyIsNotShownAtAll(t *testing.T) {
	encoded, err := json.Marshal(DealIn{ID: "d-1", Name: "Unpriced", AmountMinor: 18_000_000})
	if err != nil {
		t.Fatalf("encoding the deal: %v", err)
	}
	if strings.Contains(string(encoded), "amount") {
		t.Errorf("the prompt carries an amount with no currency: %s", encoded)
	}
}

// The won-to-date total is the same defect on a second field, and the issue
// names only the first.
func TestTheWonLifetimeTotalReachesTheModelAsAMajorUnitFigure(t *testing.T) {
	encoded, err := json.Marshal(Input{Name: "Acme", WonLifetime: 1_200_000, WonCurrency: "EUR"})
	if err != nil {
		t.Fatalf("encoding the input: %v", err)
	}
	if !strings.Contains(string(encoded), `"won_lifetime":"12000.00"`) {
		t.Errorf("the won total reaches the model as %s, want 12000.00 — the same minor-unit "+
			"misreading as the open deals, on the figure that describes the whole account", encoded)
	}
	if strings.Contains(string(encoded), "1200000") {
		t.Errorf("the won total still carries its minor-unit integer: %s", encoded)
	}
}

// A task on the timeline says whether it is finished.
//
// It reached the model twice — once under open_tasks and once here, as a
// past-dated row with no state — and nothing linked the two shapes or said the
// second was still outstanding. The model did the reasonable thing with a dated
// entry and reported the account's open tasks as completed.
func TestATaskOnTheTimelineCarriesWhetherItIsDone(t *testing.T) {
	open, done := false, true
	for _, tc := range []struct {
		name string
		act  ActIn
		want string
	}{
		{"an open task", ActIn{ID: "a-1", Kind: "task", Done: &open}, `"done":false`},
		{"a completed task", ActIn{ID: "a-2", Kind: "task", Done: &done}, `"done":true`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := json.Marshal(tc.act)
			if err != nil {
				t.Fatalf("encoding the timeline item: %v", err)
			}
			if !strings.Contains(string(encoded), tc.want) {
				t.Errorf("the timeline item reads %s, want %s", encoded, tc.want)
			}
		})
	}
}

// A kind that cannot BE finished says nothing rather than false. A call
// happened; asking whether it is done is a category error, and answering it
// invents a state the record does not have.
func TestATimelineItemThatCannotBeFinishedSaysNothingAboutIt(t *testing.T) {
	encoded, err := json.Marshal(ActIn{ID: "a-3", Kind: "call", Subject: "Discovery"})
	if err != nil {
		t.Fatalf("encoding the timeline item: %v", err)
	}
	if strings.Contains(string(encoded), "done") {
		t.Errorf("a call carries a completion state: %s", encoded)
	}
}
