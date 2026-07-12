// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

import (
	"bytes"
	"testing"

	"github.com/go-pdf/fpdf"

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

// TestRenderOfferPDF_GermanDiacriticsRenderCorrectlyNotAsMojibake is the
// non-ASCII proof the DE label suite's ASCII needles can never catch: a
// buyer legal name, a line description and the issuer name all carry
// real German diacritics (ö/ü/ß). Core Helvetica has no native UTF-8
// support, so a renderer that fed these strings to Cell unconverted
// would leave their raw UTF-8 bytes in the content stream — which a
// cp1252-expecting viewer displays as mojibake ("ö" -> "Ã¶"). This test
// asserts the OPPOSITE of that: the correctly cp1252-transcoded bytes
// are present, and the raw-UTF-8 (mojibake-precursor) bytes are absent.
func TestRenderOfferPDF_GermanDiacriticsRenderCorrectlyNotAsMojibake(t *testing.T) {
	o := testRenderOffer(100000, 19000, 119000)
	lines := []crmcontracts.OfferLineItem{
		{Position: 1, Description: "Prüfgebühr Größe", Quantity: 1, UnitPriceMinor: 100000},
	}
	buyerBlock := map[string]any{"display_name": "Müller GmbH", "legal_name": "Müller Größe & Prüfung GmbH"}
	issuerName := "Straße Verträge GmbH"

	pdf, err := RenderOfferPDF(o, lines, buyerBlock, issuerName, "de-DE")
	if err != nil {
		t.Fatalf("RenderOfferPDF() error = %v", err)
	}

	// The SAME translator RenderOfferPDF uses (built the same way, over a
	// throwaway document — UnicodeTranslatorFromDescriptor needs an
	// *Fpdf receiver but no page/content of its own) gives the expected
	// cp1252 byte form, so this test never hand-derives the encoding.
	tr := fpdf.New("P", "mm", "A4", "").UnicodeTranslatorFromDescriptor("")
	for _, want := range []string{"Prüfgebühr Größe", "Müller GmbH", "Müller Größe & Prüfung GmbH", "Straße Verträge GmbH"} {
		if !bytes.Contains(pdf, []byte(tr(want))) {
			t.Fatalf("PDF must contain the cp1252-transcoded form of %q", want)
		}
	}

	// The raw UTF-8 bytes of any diacritic (what an untranslated Cell
	// call would have left behind, and what renders as "Ã¶"-style
	// mojibake in a cp1252 viewer) must never appear.
	for _, mojibakeSeed := range []string{"ö", "ü", "ß"} {
		if bytes.Contains(pdf, []byte(mojibakeSeed)) {
			t.Fatalf("PDF must not contain the raw UTF-8 bytes of %q — that is the un-transcoded mojibake source", mojibakeSeed)
		}
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
