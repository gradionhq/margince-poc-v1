// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// The offer-drafting orchestrator's pure gates, exercised without a
// database: the conversation-price rung of the grounding ladder must
// find the price ITSELF inside the cited evidence (never just "some
// citation exists" — a real snippet with an invented price is not
// grounded), and the decimal gate must mirror the store's stricter
// exact-decimal grammar (deals.ratFromDecimal) so a candidate the store
// would reject drops here, before ever reaching AddStagedOfferLines and
// erroring the whole staging batch.

import (
	"context"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/deals"
)

// candidate builds an offerLineCandidate citing the one context item every
// test in this file seeds ("activity:1") — none of these tests exercises
// a multi-source deal, so a per-call source id would be a parameter no
// caller ever varies.
func candidate(desc, snippet string) offerLineCandidate {
	return offerLineCandidate{
		Description:     desc,
		Quantity:        "1",
		TaxRate:         "19.00",
		EvidenceSnippet: snippet,
		SourceID:        "activity:1",
	}
}

func TestGroundOfferLinesGroundsConversationPriceOnlyWhenTheAmountIsInTheCitedSnippet(t *testing.T) {
	d := offerDrafter{}
	sourceText := `Client said: "we'd want a kickoff workshop" and agreed to 20000 cents for it.`
	dealContext := []dealContextItem{{SourceID: "activity:1", Snippet: sourceText}}

	evidenced := candidate("Kickoff workshop", "agreed to 20000 cents for it")
	price := int64(20000)
	evidenced.ConversationPriceMinor = &price

	unevidenced := candidate("Follow-up session", "we'd want a kickoff workshop")
	unevidencedPrice := int64(20000)
	unevidenced.ConversationPriceMinor = &unevidencedPrice

	lines, err := d.groundOfferLines(context.Background(), []offerLineCandidate{evidenced, unevidenced}, dealContext, "EUR")
	if err != nil {
		t.Fatalf("groundOfferLines: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("staged lines = %d, want 2 (both cite real evidence; only the price grounding differs)", len(lines))
	}

	grounded := findStagedLine(t, lines, "Kickoff workshop")
	if !grounded.PriceGrounded || grounded.UnitPriceMinor != 20000 {
		t.Fatalf("Kickoff workshop price_grounded/unit_price = %v/%d, want true/20000 (the snippet states the price)",
			grounded.PriceGrounded, grounded.UnitPriceMinor)
	}

	ungrounded := findStagedLine(t, lines, "Follow-up session")
	if ungrounded.PriceGrounded || ungrounded.UnitPriceMinor != 0 {
		t.Fatalf("Follow-up session price_grounded/unit_price = %v/%d, want false/0 — the cited snippet never states 20000, only a DIFFERENT part of the source does; citing a real source is not enough, the price itself must be evidenced",
			ungrounded.PriceGrounded, ungrounded.UnitPriceMinor)
	}
}

func TestGroundOfferLinesRecognizesTheMajorUnitFormOfAnEvidencedPrice(t *testing.T) {
	d := offerDrafter{}
	dealContext := []dealContextItem{{SourceID: "activity:1", Snippet: "The client agreed to pay 200.00 for the workshop."}}

	c := candidate("Workshop", "agreed to pay 200.00 for the workshop")
	price := int64(20000) // 20000 minor units == 200.00 EUR major units
	c.ConversationPriceMinor = &price

	lines, err := d.groundOfferLines(context.Background(), []offerLineCandidate{c}, dealContext, "EUR")
	if err != nil {
		t.Fatalf("groundOfferLines: %v", err)
	}
	line := findStagedLine(t, lines, "Workshop")
	if !line.PriceGrounded || line.UnitPriceMinor != 20000 {
		t.Fatalf("price_grounded/unit_price = %v/%d, want true/20000 (major-unit form '200.00' evidences the same price)",
			line.PriceGrounded, line.UnitPriceMinor)
	}
}

func TestGroundOfferLinesHonorsAZeroDecimalCurrencysMinorUnitScale(t *testing.T) {
	d := offerDrafter{}
	// JPY has no minor-unit subdivision: 20000 minor units IS 20000 yen,
	// never "200.00" — a currency-blind /100 major-form check would miss
	// evidence that genuinely states this price.
	dealContext := []dealContextItem{{SourceID: "activity:1", Snippet: "Client agreed to 20000 yen for the workshop."}}

	c := candidate("Workshop", "agreed to 20000 yen for the workshop")
	price := int64(20000)
	c.ConversationPriceMinor = &price

	lines, err := d.groundOfferLines(context.Background(), []offerLineCandidate{c}, dealContext, "JPY")
	if err != nil {
		t.Fatalf("groundOfferLines: %v", err)
	}
	line := findStagedLine(t, lines, "Workshop")
	if !line.PriceGrounded || line.UnitPriceMinor != 20000 {
		t.Fatalf("price_grounded/unit_price = %v/%d, want true/20000 (JPY is zero-decimal; the minor integer IS the stated price)",
			line.PriceGrounded, line.UnitPriceMinor)
	}
}

func TestGroundOfferLinesDropsStoreInvalidDecimalsWithoutErroringSiblingLines(t *testing.T) {
	d := offerDrafter{}
	dealContext := []dealContextItem{{SourceID: "activity:1", Snippet: "Client wants a workshop and an onboarding session."}}

	good := candidate("Workshop", "wants a workshop")

	scientific := candidate("Onboarding (scientific)", "onboarding session")
	scientific.Quantity = "1e3"

	notANumber := candidate("Onboarding (NaN)", "onboarding session")
	notANumber.Quantity = "NaN"

	underscored := candidate("Onboarding (underscore)", "onboarding session")
	underscored.Quantity = "1_000"

	hexFloat := candidate("Onboarding (hex float)", "onboarding session")
	hexFloat.Quantity = "0x1p10"

	candidates := []offerLineCandidate{good, scientific, notANumber, underscored, hexFloat}
	lines, err := d.groundOfferLines(context.Background(), candidates, dealContext, "EUR")
	if err != nil {
		t.Fatalf("groundOfferLines: %v", err)
	}
	if len(lines) != 1 {
		t.Fatalf("staged lines = %d, want exactly 1 (only the store-valid decimal survives; the batch itself never errors)", len(lines))
	}
	if lines[0].Description != "Workshop" {
		t.Fatalf("surviving line = %q, want %q", lines[0].Description, "Workshop")
	}
}

func TestGroundOfferLinesDropsZeroAndNegativeQuantity(t *testing.T) {
	d := offerDrafter{}
	dealContext := []dealContextItem{{SourceID: "activity:1", Snippet: "Client wants a workshop."}}

	zero := candidate("Zero qty", "wants a workshop")
	zero.Quantity = "0"
	zeroDecimal := candidate("Zero decimal qty", "wants a workshop")
	zeroDecimal.Quantity = "0.00"
	negative := candidate("Negative qty", "wants a workshop")
	negative.Quantity = "-1"

	lines, err := d.groundOfferLines(context.Background(), []offerLineCandidate{zero, zeroDecimal, negative}, dealContext, "EUR")
	if err != nil {
		t.Fatalf("groundOfferLines: %v", err)
	}
	if len(lines) != 0 {
		t.Fatalf("staged lines = %d, want 0 (zero/negative quantity must never stage)", len(lines))
	}
}

func TestValidDecimalMirrorsTheStoresExactDecimalGrammar(t *testing.T) {
	cases := []struct {
		name string
		in   string
		lo   float64
		hi   float64
		want bool
	}{
		{"plain integer", "1", 0, 1e12, true},
		{"plain decimal", "19.00", 0, 100, true},
		{"negative sign accepted by the grammar", "-1", -1e12, 1e12, true},
		{"scientific notation rejected", "1e3", 0, 1e12, false},
		{"NaN rejected", "NaN", 0, 1e12, false},
		{"hex float rejected", "0x1p10", 0, 1e12, false},
		{"underscore digit separator rejected", "1_000", 0, 1e12, false},
		{"out of bounds", "200", 0, 100, false},
		{"empty string rejected", "", 0, 1e12, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, ok := validDecimal(tc.in, tc.lo, tc.hi)
			if ok != tc.want {
				t.Fatalf("validDecimal(%q, %v, %v) ok = %v, want %v", tc.in, tc.lo, tc.hi, ok, tc.want)
			}
		})
	}
}

func findStagedLine(t *testing.T, lines []deals.StagedOfferLineInput, description string) deals.StagedOfferLineInput {
	t.Helper()
	for _, l := range lines {
		if l.Description == description {
			return l
		}
	}
	t.Fatalf("no staged line with description %q among %d lines", description, len(lines))
	return deals.StagedOfferLineInput{}
}
