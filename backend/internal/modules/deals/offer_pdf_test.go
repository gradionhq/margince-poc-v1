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

	pdf, err := RenderOfferPDF(o, testRenderLines(), buyerBlock, "Margince GmbH", "de-DE", nil)
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

	dePDF, err := RenderOfferPDF(o, lines, nil, "Margince GmbH", "de-DE", nil)
	if err != nil {
		t.Fatalf("RenderOfferPDF(de-DE) error = %v", err)
	}
	enPDF, err := RenderOfferPDF(o, lines, nil, "Margince GmbH", "en", nil)
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

	pdf, err := RenderOfferPDF(o, testRenderLines(), nil, "Margince GmbH", "de-DE", nil)
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

	pdf, err := RenderOfferPDF(o, lines, buyerBlock, issuerName, "de-DE", nil)
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

	withBuyer, err := RenderOfferPDF(o, testRenderLines(), map[string]any{"display_name": "Acme GmbH"}, "Margince GmbH", "de-DE", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(withBuyer, []byte("Kunde")) {
		t.Fatalf("a non-nil buyer block must render the buyer section heading:\n%s", withBuyer)
	}

	withoutBuyer, err := RenderOfferPDF(o, testRenderLines(), nil, "Margince GmbH", "de-DE", nil)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(withoutBuyer, []byte("Kunde")) {
		t.Fatalf("a nil buyer block must omit the buyer section entirely:\n%s", withoutBuyer)
	}
}

// TestRenderOfferPDF_TwoTemplatesWithDistinctLayoutsProduceDifferentBytes
// is the layout-actually-renders proof: two templates whose layout bags
// differ only in their header/footer/terms text must produce genuinely
// different PDF bytes — the regression this guards is a renderer that
// resolves a template (for its locale) but silently ignores the layout it
// carries, so every template would look identical regardless of its
// configured branding.
func TestRenderOfferPDF_TwoTemplatesWithDistinctLayoutsProduceDifferentBytes(t *testing.T) {
	o := testRenderOffer(100000, 19000, 119000)
	lines := testRenderLines()

	layoutA := map[string]any{"header_text": "Alpha Consulting GmbH", "footer_text": "Alpha footer", "terms_text": "Alpha terms apply"}
	layoutB := map[string]any{"header_text": "Beta Solutions GmbH", "footer_text": "Beta footer", "terms_text": "Beta terms apply"}

	pdfA, err := RenderOfferPDF(o, lines, nil, "Margince GmbH", "de-DE", layoutA)
	if err != nil {
		t.Fatalf("RenderOfferPDF(layoutA) error = %v", err)
	}
	pdfB, err := RenderOfferPDF(o, lines, nil, "Margince GmbH", "de-DE", layoutB)
	if err != nil {
		t.Fatalf("RenderOfferPDF(layoutB) error = %v", err)
	}
	if bytes.Equal(pdfA, pdfB) {
		t.Fatal("two templates with distinct layouts must produce different PDF bytes — the layout is being ignored")
	}

	for _, want := range []string{"Alpha Consulting GmbH", "Alpha footer", "Alpha terms apply"} {
		if !bytes.Contains(pdfA, []byte(want)) {
			t.Fatalf("layoutA's PDF must contain %q:\n%s", want, pdfA)
		}
	}
	for _, unwanted := range []string{"Beta Solutions GmbH", "Beta footer", "Beta terms apply"} {
		if bytes.Contains(pdfA, []byte(unwanted)) {
			t.Fatalf("layoutA's PDF must not contain layoutB's text %q", unwanted)
		}
	}
	for _, want := range []string{"Beta Solutions GmbH", "Beta footer", "Beta terms apply"} {
		if !bytes.Contains(pdfB, []byte(want)) {
			t.Fatalf("layoutB's PDF must contain %q:\n%s", want, pdfB)
		}
	}
}

// TestRenderOfferPDF_EmptyLayoutOmitsHeaderFooterTermsSections proves the
// honest-gap side of the contract: a template with no header/footer/terms
// text (or no template at all — a nil layout) renders exactly the base
// document, with no empty "Terms" heading or stray blank lines standing
// in for the sections layout would otherwise add.
func TestRenderOfferPDF_EmptyLayoutOmitsHeaderFooterTermsSections(t *testing.T) {
	o := testRenderOffer(100000, 19000, 119000)

	pdf, err := RenderOfferPDF(o, testRenderLines(), nil, "Margince GmbH", "de-DE", nil)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(pdf, []byte("Bedingungen")) {
		t.Fatalf("a nil layout must omit the terms heading entirely:\n%s", pdf)
	}
}

// TestRenderOfferPDF_LayoutIgnoresNonStringAndUnknownKeys proves this
// renderer only ever honors the bounded string keys it documents: an
// unknown key (a future/decorative ref like logo_url) and a non-string
// value under a known key are both silently ignored rather than panicking
// or leaking a Go-formatted value into the document.
func TestRenderOfferPDF_LayoutIgnoresNonStringAndUnknownKeys(t *testing.T) {
	o := testRenderOffer(100000, 19000, 119000)
	layout := map[string]any{
		"logo_url":    "https://example.test/logo.png",
		"header_text": 12345, // wrong type — must be ignored, not stringified
	}

	pdf, err := RenderOfferPDF(o, testRenderLines(), nil, "Margince GmbH", "de-DE", layout)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(pdf, []byte("logo_url")) || bytes.Contains(pdf, []byte("example.test")) {
		t.Fatalf("an unhonored layout key must never leak into the document:\n%s", pdf)
	}
	if bytes.Contains(pdf, []byte("12345")) {
		t.Fatalf("a non-string value under a known layout key must be ignored, not stringified:\n%s", pdf)
	}
}
