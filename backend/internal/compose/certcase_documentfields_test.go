// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// How a scenario decides one value is right, which is not one question but two:
// three of the four fields have exactly one correct answer, and the deal name is
// prose. Grading the name by exact string measured phrasing rather than reading
// — a real gemini run read a heading-prefixed name off the fixture's own words
// and was scored wrong for it.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/ports/extraction"
)

func TestTheStructuredFieldsAreComparedExactly(t *testing.T) {
	for _, field := range []string{documentFieldAmount, documentFieldCurrency, documentFieldClose} {
		if valueAgrees(field, "14850000", "1485000") {
			t.Errorf("%s: a value off by a factor of ten agreed", field)
		}
		if !valueAgrees(field, "EUR", "EUR") {
			t.Errorf("%s: an exact match disagreed", field)
		}
		// Containment must NOT leak into these: "148500" inside "14850000" is
		// the hundredfold error this whole site exists to catch.
		if valueAgrees(field, "14850000", "148500") {
			t.Errorf("%s: a contained value agreed, so the amount check is not exact", field)
		}
	}
}

func TestTheDealNameIsGradedOnWhatItNamesNotHowItIsWorded(t *testing.T) {
	want := "Pallet Handling Programme, Graz site"
	for _, got := range []string{
		"Pallet Handling Programme, Graz site",
		"Order Form — Pallet Handling Programme, Graz site",
		"pallet handling programme, graz site",
	} {
		if !valueAgrees(documentFieldName, got, want) {
			t.Errorf("name %q was scored wrong against %q — that is phrasing, not reading", got, want)
		}
	}
	// It still has to name the right thing. The customer and the reference code
	// both sit within a few lines of the name on that fixture, and reporting
	// either is a misread rather than a rewording.
	for _, got := range []string{"Steirische Molkerei AG", "SM-2027-Q1-PALLET", ""} {
		if valueAgrees(documentFieldName, got, want) {
			t.Errorf("name %q was accepted against %q", got, want)
		}
	}
}

// The whole-reading comparison still refuses an invented field, which is the
// failure that puts a number on a deal nobody agreed to.
func TestAnInventedFieldIsAlwaysADisagreement(t *testing.T) {
	c := &documentFieldsCase{expected: map[string]string{documentFieldName: "Pallet Handling Programme"}}
	got := c.disagreements([]extraction.ExtractedField{
		{Field: documentFieldName, Value: "Pallet Handling Programme"},
		{Field: documentFieldAmount, Value: "999900"},
	})
	if len(got) != 1 {
		t.Fatalf("disagreements = %v, want exactly the invented amount", got)
	}
}
