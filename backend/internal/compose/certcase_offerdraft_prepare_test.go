// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// What the offer-draft case refuses before a paid run spends anything on it: an
// expectation no draft could satisfy, a fixture the orchestrator could never
// have handed this call, and — the seam that makes the price ladder runnable at
// all — a rate card that answers an id it never held the way the store does.

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/database/storekit"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// An expectation no draft could satisfy would measure nothing for as long as it
// stayed in the corpus. Prepare is where that gets named, while it is still a
// wiring error rather than a paid run of zeros.
func TestOfferDraftCaseRefusesAnUnreachableExpectation(t *testing.T) {
	cases := []struct {
		name     string
		expected json.RawMessage
		wantMsg  string
	}{
		{
			name:     "no line expected at all",
			expected: json.RawMessage(`{}`),
			wantMsg:  "expects no line",
		},
		{
			name:     "an expectation shaped like something else",
			expected: json.RawMessage(`["activity:1"]`),
			wantMsg:  "context source id to the line it grounds",
		},
		{
			name:     "no expectation at all",
			expected: nil,
			wantMsg:  "context source id to the line it grounds",
		},
		{
			name: "a citation the fixture never captures",
			expected: offerDraftExpectation(t, map[string]offerDraftExpectedLine{
				"activity:99": {UnitPriceMinor: 20000, PriceGrounded: true},
			}),
			wantMsg: "which the fixture never captures",
		},
		{
			name: "an ungrounded line carrying a price",
			expected: offerDraftExpectation(t, map[string]offerDraftExpectedLine{
				offerDraftKickoffSource: {UnitPriceMinor: 20000},
			}),
			wantMsg: "prices every ungrounded line at zero",
		},
		{
			// Neither rung can reach it: the cited conversation does not state the
			// amount, and no product in the offer's currency charges it.
			name: "a grounded price nothing in the fixture carries",
			expected: offerDraftExpectation(t, map[string]offerDraftExpectedLine{
				offerDraftSupportSource: {UnitPriceMinor: 12345, PriceGrounded: true},
			}),
			wantMsg: "neither the cited context states nor any EUR rate-card product charges",
		},
		{
			// The product exists and is priced at 90000 — in USD, which the ladder
			// refuses to price this offer across.
			name: "a grounded price only another currency's product carries",
			expected: offerDraftExpectation(t, map[string]offerDraftExpectedLine{
				offerDraftSupportSource: {UnitPriceMinor: 90000, PriceGrounded: true},
			}),
			wantMsg: "neither the cited context states nor any EUR rate-card product charges",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := offerDraftCases{}.Prepare(offerDraftFixtureJSON(t, offerDraftDealFixture()), tc.expected)
			if err == nil {
				t.Fatal("an unreachable expectation prepared")
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("the refusal does not say what is unreachable: %v", err)
			}
		})
	}
}

// A fixture the orchestrator could never have handed this call would certify a
// prompt the product does not send.
func TestOfferDraftCaseRefusesAFixtureTheOrchestratorCouldNotHandIt(t *testing.T) {
	oversized := offerDraftDealFixture()
	for i := len(oversized.ContextItems); i <= offerDraftContextItems; i++ {
		oversized.ContextItems = append(oversized.ContextItems,
			offerDraftContextItem{SourceID: "activity:" + strconv.Itoa(i), Snippet: "Another captured line."})
	}
	cases := []struct {
		name    string
		mutate  func(f *offerDraftFixture)
		wantMsg string
	}{
		{
			name:    "an offer with no currency",
			mutate:  func(f *offerDraftFixture) { f.Currency = " " },
			wantMsg: "names no currency",
		},
		{
			name:    "a deal with no captured context",
			mutate:  func(f *offerDraftFixture) { f.ContextItems = nil },
			wantMsg: "captures no deal context",
		},
		{
			name: "a context item missing its evidence",
			mutate: func(f *offerDraftFixture) {
				f.ContextItems[0].Snippet = "  "
			},
			wantMsg: "without both an id and its text",
		},
		{
			name: "two context items under one id",
			mutate: func(f *offerDraftFixture) {
				f.ContextItems[1].SourceID = offerDraftKickoffSource
			},
			wantMsg: "share the id",
		},
		{
			name: "a rate-card entry no products table holds",
			mutate: func(f *offerDraftFixture) {
				f.RateCard[0].Currency = ""
			},
			wantMsg: "without a name or a currency",
		},
		{
			name:    "more context than the assembly returns",
			mutate:  func(f *offerDraftFixture) { f.ContextItems = oversized.ContextItems },
			wantMsg: "this call is handed at most",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := offerDraftDealFixture()
			tc.mutate(&f)

			_, err := offerDraftCases{}.Prepare(offerDraftFixtureJSON(t, f), offerDraftKickoffExpected(t))

			if err == nil {
				t.Fatal("a fixture the orchestrator could not hand this call prepared")
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("the refusal does not name what is wrong: %v", err)
			}
		})
	}
}

// The rate-card seam exists so the ladder can be certified without a database,
// and the ladder tells "no such product" from a real fault: it grounds nothing
// on the first and aborts the whole draft on the second. A fixture rate card
// that answered an unheld id with anything else would make the invented-id case
// above measure the wrong thing.
func TestOfferDraftRateCardAnswersAnUnheldIDTheWayTheStoreDoes(t *testing.T) {
	c := prepareOfferDraftCase(t, offerDraftDealFixture(), offerDraftSupportExpected(t))
	held := ids.From[ids.ProductKind](ids.UUID(c.catalog[0].Id))

	product, err := c.drafter.rateCard.GetProduct(context.Background(), held, storekit.LiveOnly)
	if err != nil {
		t.Fatalf("the rate card does not hold the product it was built from: %v", err)
	}
	if product.Name != offerDraftSupportPlan || product.UnitPriceMinor != 5000 {
		t.Errorf("the rate card answered %+v, want the fixture's own product", product)
	}

	_, err = c.drafter.rateCard.GetProduct(context.Background(), ids.New[ids.ProductKind](), storekit.LiveOnly)

	if !errors.Is(err, apperrors.ErrNotFound) {
		t.Errorf("an id the catalogue never carried answered %v, want the store's own not-found", err)
	}
}
