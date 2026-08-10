// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package accountdraft

// What the draft is told the deal is worth.
//
// This prompt writes an outbound message to the customer, which makes it the
// more consequential half of the account brief's defect (#591): the brief
// misreads a number on our own screen, this one can put the misread number in
// front of the person paying it.

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTheDealAmountReachesTheDraftAsAMajorUnitFigure(t *testing.T) {
	for _, tc := range []struct {
		name     string
		minor    int64
		currency string
		want     string
		reject   string
	}{
		{"a two-decimal currency", 18_000_000, "EUR", `"amount":"180000.00"`, "18000000"},
		// The case `/100` gets wrong: yen has no minor unit, so the integer IS
		// the figure and dividing understates it a hundredfold.
		{"a zero-decimal currency", 18_000_000, "JPY", `"amount":"18000000"`, "180000.00"},
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
				t.Errorf("the draft prompt carries %s, want %s — the model writes this figure into "+
					"a message the customer reads", got, tc.want)
			}
			if strings.Contains(got, tc.reject) {
				t.Errorf("the draft prompt still carries %q in %s, which is the same money at the "+
					"wrong scale", tc.reject, got)
			}
		})
	}
}

// An amount with no currency has no scale, so it is not shown: a figure printed
// without its code is a number whose scale the reader has to guess.
func TestADraftAmountWithNoCurrencyIsNotShownAtAll(t *testing.T) {
	encoded, err := json.Marshal(DealIn{ID: "d-1", Name: "Unpriced", AmountMinor: 18_000_000})
	if err != nil {
		t.Fatalf("encoding the deal: %v", err)
	}
	if strings.Contains(string(encoded), "amount") {
		t.Errorf("the draft prompt carries an amount with no currency: %s", encoded)
	}
}
