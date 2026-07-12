// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The offer PDF renderer: a branded A4 document — title/buyer/line-items/
// totals, plus the selected template's layout text (header/footer/terms,
// when the template's layout carries them) — built purely from already-
// resolved, already-persisted inputs (offer_render.go's PrepareRender
// gathers them). The offer's Net/Tax/GrossMinor fields are the totals
// engine's already-persisted output (offer_totals.go, recomputed inside
// every mutating transaction); this file reads them off the Offer struct
// as-is and performs no money arithmetic of its own, so the rendered
// document can never disagree with what the API already shows.
//
// layout is a workspace-authored, loosely-typed jsonb bag (logo/header/
// footer/terms-block refs — crm.yaml's OfferTemplate.layout). This
// renderer honors exactly three string keys — header_text, footer_text,
// terms_text — printing each verbatim when present and omitting the
// section entirely when absent; every other key (including a logo
// reference) is a decorative ref this offline, network-free renderer
// does not fetch or embed. That is the bounded, honest slice of "branded
// layout" V1 ships; a richer template engine is future scope, not a
// build-side invention of new keys.

package deals

import (
	"bytes"
	"fmt"
	"strconv"

	"github.com/go-pdf/fpdf"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

// pdfLabels holds one locale's label set for the rendered PDF.
type pdfLabels struct {
	title     string
	issuer    string
	revision  string
	buyer     string
	lineItems string
	net       string
	tax       string
	gross     string
	totals    string
	terms     string
}

// resolvePDFLabels maps a locale to its label set: de-DE (and the empty
// string, matching offer_template's own defaultOfferTemplateLocale) get
// German labels; everything else gets English — the DE/EN launch pair
// (WP7/OP-T02), not a general i18n catalog.
func resolvePDFLabels(locale string) pdfLabels {
	if locale == defaultOfferTemplateLocale || locale == "" {
		return pdfLabels{
			title:     "Angebot",
			issuer:    "Aussteller",
			revision:  "Revision",
			buyer:     "Kunde",
			lineItems: "Positionen",
			net:       "Nettobetrag",
			tax:       "MwSt",
			gross:     "Gesamtbetrag",
			totals:    "Summe",
			terms:     "Bedingungen",
		}
	}
	return pdfLabels{
		title:     "Offer",
		issuer:    "Issuer",
		revision:  "Revision",
		buyer:     "Buyer",
		lineItems: "Line items",
		net:       "Net",
		tax:       "Tax",
		gross:     "Total",
		totals:    "Totals",
		terms:     "Terms",
	}
}

// pdfFormatMinor renders an integer minor-unit amount as a decimal
// display string with its currency code. Display-only: the money engine
// itself (offer_totals.go) never touches a float, and this function never
// re-derives the amount it formats.
func pdfFormatMinor(minor int64, currency string) string {
	sign := ""
	if minor < 0 {
		sign = "-"
		minor = -minor
	}
	return fmt.Sprintf("%s%d.%02d %s", sign, minor/100, minor%100, currency)
}

func pdfFormatQuantity(quantity float64) string {
	return strconv.FormatFloat(quantity, 'f', -1, 64)
}

func pdfBuyerBlockString(buyerBlock map[string]any, key string) string {
	v, _ := buyerBlock[key].(string)
	return v
}

// pdfLayoutString reads one bounded string key off a template's layout
// bag (see this file's doc comment for the honored keys); an absent key,
// or one holding a non-string value, answers "" — the caller's cue to
// omit that section entirely rather than print an empty heading.
func pdfLayoutString(layout map[string]any, key string) string {
	v, _ := layout[key].(string)
	return v
}

// pdfTranslator converts a UTF-8 Go string to the byte sequence the core
// Helvetica font's built-in cp1252 encoding expects, so Cell/MultiCell
// render diacritics correctly instead of the raw UTF-8 bytes' mojibake
// (e.g. "ö" as two separate cp1252 code points, "Ã¶"). Every function in
// this file threads it through and applies it to every string it hands
// to Cell — labels included, since translating plain ASCII is a no-op.
type pdfTranslator func(string) string

// writeOfferPDFHeader writes the title/revision/issuer block, the
// template layout's header_text (when the layout carries one), and, when
// buyerBlock is non-nil, the buyer legal block underneath it. A nil
// buyerBlock (an unsent draft with no buyer org) omits the section
// entirely rather than printing an empty heading.
func writeOfferPDFHeader(pdf *fpdf.Fpdf, tr pdfTranslator, o crmcontracts.Offer, buyerBlock map[string]any, issuerName string, layout map[string]any, labels pdfLabels) {
	number := ""
	if o.OfferNumber != nil {
		number = *o.OfferNumber
	}
	revision := 0
	if o.Revision != nil {
		revision = *o.Revision
	}

	pdf.SetFont("Helvetica", "B", 18)
	pdf.Cell(0, 8, tr(labels.title+" "+number))
	pdf.Ln(10)
	pdf.SetFont("Helvetica", "", 11)
	pdf.Cell(0, 6, tr(labels.revision+" "+strconv.Itoa(revision)))
	pdf.Ln(7)
	pdf.Cell(0, 6, tr(labels.issuer+": "+issuerName))
	pdf.Ln(10)

	if headerText := pdfLayoutString(layout, "header_text"); headerText != "" {
		pdf.SetFont("Helvetica", "", 10)
		pdf.MultiCell(0, 5, tr(headerText), "", "L", false)
		pdf.Ln(4)
	}

	if buyerBlock == nil {
		return
	}
	pdf.SetFont("Helvetica", "B", 12)
	pdf.Cell(0, 6, tr(labels.buyer))
	pdf.Ln(7)
	pdf.SetFont("Helvetica", "", 11)
	if id := pdfBuyerBlockString(buyerBlock, "organization_id"); id != "" {
		pdf.Cell(0, 6, tr("Organization ID: "+id))
		pdf.Ln(6)
	}
	if displayName := pdfBuyerBlockString(buyerBlock, "display_name"); displayName != "" {
		pdf.Cell(0, 6, tr(displayName))
		pdf.Ln(6)
	}
	if legalName := pdfBuyerBlockString(buyerBlock, "legal_name"); legalName != "" {
		pdf.Cell(0, 6, tr(legalName))
		pdf.Ln(6)
	}
	pdf.Ln(4)
}

// writeOfferPDFLineItems writes the line-items section. Each line's
// displayed unit price is its own persisted snapshot (offer_lines.go);
// this function shows it verbatim, deriving nothing.
func writeOfferPDFLineItems(pdf *fpdf.Fpdf, tr pdfTranslator, lines []crmcontracts.OfferLineItem, currency string, labels pdfLabels) {
	pdf.SetFont("Helvetica", "B", 12)
	pdf.Cell(0, 6, tr(labels.lineItems))
	pdf.Ln(7)
	pdf.SetFont("Helvetica", "", 10)
	for _, li := range lines {
		pdf.Cell(0, 5, tr(fmt.Sprintf("%d. %s", li.Position, li.Description)))
		pdf.Ln(5)
		pdf.Cell(0, 5, tr(fmt.Sprintf("%s x %s", pdfFormatQuantity(li.Quantity), pdfFormatMinor(li.UnitPriceMinor, currency))))
		pdf.Ln(5)
	}
	pdf.Ln(2)
}

// writeOfferPDFTotals writes the money summary straight off o's already-
// persisted Net/Tax/GrossMinor — the ONE totals figure this function (or
// any part of this renderer) ever shows; nothing here re-sums the lines.
func writeOfferPDFTotals(pdf *fpdf.Fpdf, tr pdfTranslator, o crmcontracts.Offer, labels pdfLabels) {
	var net, tax, gross int64
	if o.NetMinor != nil {
		net = *o.NetMinor
	}
	if o.TaxMinor != nil {
		tax = *o.TaxMinor
	}
	if o.GrossMinor != nil {
		gross = *o.GrossMinor
	}
	pdf.SetFont("Helvetica", "B", 12)
	pdf.Cell(0, 6, tr(labels.totals))
	pdf.Ln(7)
	pdf.SetFont("Helvetica", "", 11)
	pdf.Cell(0, 6, tr(labels.net+": "+pdfFormatMinor(net, o.Currency)))
	pdf.Ln(6)
	pdf.Cell(0, 6, tr(labels.tax+": "+pdfFormatMinor(tax, o.Currency)))
	pdf.Ln(6)
	pdf.Cell(0, 6, tr(labels.gross+": "+pdfFormatMinor(gross, o.Currency)))
	pdf.Ln(6)
}

// writeOfferPDFTerms writes the template layout's terms_text as a
// dedicated section, when the layout carries one — omitted entirely
// otherwise, never an empty heading.
func writeOfferPDFTerms(pdf *fpdf.Fpdf, tr pdfTranslator, layout map[string]any, labels pdfLabels) {
	text := pdfLayoutString(layout, "terms_text")
	if text == "" {
		return
	}
	pdf.Ln(2)
	pdf.SetFont("Helvetica", "B", 12)
	pdf.Cell(0, 6, tr(labels.terms))
	pdf.Ln(7)
	pdf.SetFont("Helvetica", "", 10)
	pdf.MultiCell(0, 5, tr(text), "", "L", false)
}

// writeOfferPDFFooter writes the template layout's footer_text at the
// bottom of the rendered content, when the layout carries one.
func writeOfferPDFFooter(pdf *fpdf.Fpdf, tr pdfTranslator, layout map[string]any) {
	text := pdfLayoutString(layout, "footer_text")
	if text == "" {
		return
	}
	pdf.Ln(6)
	pdf.SetFont("Helvetica", "I", 9)
	pdf.MultiCell(0, 5, tr(text), "", "L", false)
}

// RenderOfferPDF builds the branded offer PDF from already-resolved
// inputs (see offer_render.go's PrepareRender): o carries the offer
// header and its server-computed totals, lines are its accepted line
// items, buyerBlock is the frozen buyer_snapshot once sent or the live
// buyer organization while still draft (nil when the offer has none),
// issuerName is the seller's display name, locale drives the DE/EN label
// set, and layout is the selected template's layout bag (nil or empty
// when the offer carries no template) — see this file's doc comment for
// the bounded set of layout keys honored. This function performs no
// database access and no money arithmetic — every figure it prints was
// computed and persisted before it was called, so the rendered document
// can never disagree with the server-computed totals.
func RenderOfferPDF(o crmcontracts.Offer, lines []crmcontracts.OfferLineItem, buyerBlock map[string]any, issuerName, locale string, layout map[string]any) ([]byte, error) {
	labels := resolvePDFLabels(locale)
	number := ""
	if o.OfferNumber != nil {
		number = *o.OfferNumber
	}

	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetCompression(false)
	pdf.SetMargins(16, 16, 16)
	pdf.SetAutoPageBreak(true, 16)
	// tr maps a UTF-8 string onto core Helvetica's built-in cp1252
	// encoding — the launch DE/EN locale pair's Latin diacritics (ö, ü,
	// ß, é, …) all round-trip through cp1252; a non-Latin locale (e.g.
	// Cyrillic, CJK) would need an embedded TTF font instead, not
	// attempted here since only DE/EN ship at launch.
	tr := pdf.UnicodeTranslatorFromDescriptor("")
	// The isUTF8=true argument (unlike Cell's cp1252 byte stream) tells
	// fpdf to UTF-16BE-encode the metadata itself, so the document
	// properties survive a non-ASCII issuer/title too.
	pdf.SetTitle(labels.title+" "+number, true)
	pdf.SetCreator("margince", true)
	pdf.SetAuthor(issuerName, true)
	pdf.SetSubject(labels.title+" PDF", true)
	pdf.AddPage()

	writeOfferPDFHeader(pdf, tr, o, buyerBlock, issuerName, layout, labels)
	writeOfferPDFLineItems(pdf, tr, lines, o.Currency, labels)
	writeOfferPDFTotals(pdf, tr, o, labels)
	writeOfferPDFTerms(pdf, tr, layout, labels)
	writeOfferPDFFooter(pdf, tr, layout)

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, fmt.Errorf("render offer pdf: %w", err)
	}
	return buf.Bytes(), nil
}
