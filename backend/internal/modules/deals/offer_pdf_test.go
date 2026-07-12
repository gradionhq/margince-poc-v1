// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

import (
	"bytes"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

// testRenderOffer builds a minimal but complete offer for the PDF unit
// tests: two lines, an explicit currency, and the money triple already
// "persisted" as the totals engine would leave it.
func testRenderOffer(net, tax, gross int64) crmcontracts.Offer {
	number := "A-1042"
	revision := 2
	return crmcontracts.Offer{
		OfferNumber: &number,
		Revision:    &revision,
		Currency:    "EUR",
		NetMinor:    &net,
		TaxMinor:    &tax,
		GrossMinor:  &gross,
	}
}

func testRenderLines() []crmcontracts.OfferLineItem {
	return []crmcontracts.OfferLineItem{
		{Position: 1, Description: "Consulting Day", Quantity: 2, UnitPriceMinor: 50000},
		{Position: 2, Description: "Setup Fee", Quantity: 1, UnitPriceMinor: 23456},
	}
}

func TestRenderOfferPDF_IncludesOfferDataAndStoredTotals(t *testing.T) {
	o := testRenderOffer(123456, 23456, 146912)
	buyerBlock := map[string]any{"organization_id": "org-1", "display_name": "Acme GmbH"}

	pdf, err := RenderOfferPDF(o, testRenderLines(), buyerBlock, "Margince GmbH", "de-DE")
	if err != nil {
		t.Fatalf("RenderOfferPDF() error = %v", err)
	}
	if len(pdf) == 0 {
		t.Fatal("RenderOfferPDF() returned an empty PDF")
	}

	mustContain := []string{
		"A-1042", "Revision 2", "Acme GmbH", "Margince GmbH",
		"Consulting Day", "2 x 500.00 EUR", "Setup Fee",
		"1234.56 EUR", "234.56 EUR", "1469.12 EUR",
	}
	for _, needle := range mustContain {
		if !bytes.Contains(pdf, []byte(needle)) {
			t.Fatalf("PDF missing %q", needle)
		}
	}
}

func TestRenderOfferPDF_LocaleDrivesLabels(t *testing.T) {
	o := testRenderOffer(100000, 19000, 119000)
	lines := testRenderLines()

	dePDF, err := RenderOfferPDF(o, lines, nil, "Margince GmbH", "de-DE")
	if err != nil {
		t.Fatalf("RenderOfferPDF(de-DE) error = %v", err)
	}
	enPDF, err := RenderOfferPDF(o, lines, nil, "Margince GmbH", "en")
	if err != nil {
		t.Fatalf("RenderOfferPDF(en) error = %v", err)
	}

	if !bytes.Contains(dePDF, []byte("Angebot")) || !bytes.Contains(dePDF, []byte("Nettobetrag")) {
		t.Fatalf("de-DE PDF missing German labels:\n%s", dePDF)
	}
	if bytes.Contains(dePDF, []byte("Offer ")) {
		t.Fatalf("de-DE PDF unexpectedly contains the English title label:\n%s", dePDF)
	}

	if !bytes.Contains(enPDF, []byte("Offer ")) || !bytes.Contains(enPDF, []byte("Net: ")) {
		t.Fatalf("en PDF missing English labels:\n%s", enPDF)
	}
	if bytes.Contains(enPDF, []byte("Angebot")) {
		t.Fatalf("en PDF unexpectedly contains a German label:\n%s", enPDF)
	}
}

// TestRenderOfferPDF_UsesStoredTotalsNeverRecomputes is the OFFER-AC-12a
// no-drift proof: the offer's stored NetMinor/TaxMinor/GrossMinor are
// deliberately set to a figure that does NOT match what re-summing the
// two lines below would produce (2×500.00 + 234.56 = 1234.56 net, not
// the 999999 minor units set here). The rendered PDF must show the
// STORED figure verbatim — proving RenderOfferPDF never re-derives
// totals from the lines, only reads what the caller already computed
// and persisted.
func TestRenderOfferPDF_UsesStoredTotalsNeverRecomputes(t *testing.T) {
	mismatchedNet := int64(999999)
	o := testRenderOffer(mismatchedNet, 1, 1000000)

	pdf, err := RenderOfferPDF(o, testRenderLines(), nil, "Margince GmbH", "de-DE")
	if err != nil {
		t.Fatalf("RenderOfferPDF() error = %v", err)
	}
	if !bytes.Contains(pdf, []byte("9999.99 EUR")) {
		t.Fatalf("PDF must render the offer's STORED net total (9999.99 EUR), got:\n%s", pdf)
	}
	if bytes.Contains(pdf, []byte("1234.56 EUR")) {
		t.Fatalf("PDF must NOT contain a freshly re-derived total from the lines (1234.56 EUR); it must use the stored figure only:\n%s", pdf)
	}
}

func TestRenderOfferPDF_OmitsBuyerSectionWhenBuyerBlockNil(t *testing.T) {
	o := testRenderOffer(100000, 19000, 119000)

	withBuyer, err := RenderOfferPDF(o, testRenderLines(), map[string]any{"display_name": "Acme GmbH"}, "Margince GmbH", "de-DE")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(withBuyer, []byte("Kunde")) {
		t.Fatalf("a non-nil buyer block must render the buyer section heading:\n%s", withBuyer)
	}

	withoutBuyer, err := RenderOfferPDF(o, testRenderLines(), nil, "Margince GmbH", "de-DE")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(withoutBuyer, []byte("Kunde")) {
		t.Fatalf("a nil buyer block must omit the buyer section entirely:\n%s", withoutBuyer)
	}
}
