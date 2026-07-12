// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The offer PDF renderer (OFFER-AC-12/12a, WP7/OP-T06, offers-depth arc
// 4a T4): a branded A4 document — title/buyer/line-items/totals — built
// purely from already-resolved, already-persisted inputs (offer_render.go's
// PrepareRender gathers them). The offer's Net/Tax/GrossMinor fields are
// the totals engine's already-persisted output (offer_totals.go,
// recomputed inside every mutating transaction); this file reads them off
// the Offer struct as-is and performs no money arithmetic of its own, so
// the rendered document can never disagree with what the API already
// shows (OFFER-AC-12a: no drift between the rendered figures and the
// server-computed ones).

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
	buyer     string
	lineItems string
	net       string
	tax       string
	gross     string
}

// resolvePDFLabels maps a locale to its label set: de-DE (and the empty
// string, matching offer_template's own defaultOfferTemplateLocale) get
// German labels; everything else gets English — the DE/EN launch pair
// (WP7/OP-T02), not a general i18n catalog.
func resolvePDFLabels(locale string) pdfLabels {
	if locale == defaultOfferTemplateLocale || locale == "" {
		return pdfLabels{
			title:     "Angebot",
			buyer:     "Kunde",
			lineItems: "Positionen",
			net:       "Nettobetrag",
			tax:       "MwSt",
			gross:     "Gesamtbetrag",
		}
	}
	return pdfLabels{
		title:     "Offer",
		buyer:     "Buyer",
		lineItems: "Line items",
		net:       "Net",
		tax:       "Tax",
		gross:     "Total",
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

// writeOfferPDFHeader writes the title/revision/issuer block and, when
// buyerBlock is non-nil, the buyer legal block underneath it. A nil
// buyerBlock (an unsent draft with no buyer org) omits the section
// entirely rather than printing an empty heading.
func writeOfferPDFHeader(pdf *fpdf.Fpdf, o crmcontracts.Offer, buyerBlock map[string]any, issuerName string, labels pdfLabels) {
	number := ""
	if o.OfferNumber != nil {
		number = *o.OfferNumber
	}
	revision := 0
	if o.Revision != nil {
		revision = *o.Revision
	}

	pdf.SetFont("Helvetica", "B", 18)
	pdf.Cell(0, 8, labels.title+" "+number)
	pdf.Ln(10)
	pdf.SetFont("Helvetica", "", 11)
	pdf.Cell(0, 6, "Revision "+strconv.Itoa(revision))
	pdf.Ln(7)
	pdf.Cell(0, 6, "Issuer: "+issuerName)
	pdf.Ln(10)

	if buyerBlock == nil {
		return
	}
	pdf.SetFont("Helvetica", "B", 12)
	pdf.Cell(0, 6, labels.buyer)
	pdf.Ln(7)
	pdf.SetFont("Helvetica", "", 11)
	if id := pdfBuyerBlockString(buyerBlock, "organization_id"); id != "" {
		pdf.Cell(0, 6, "Organization ID: "+id)
		pdf.Ln(6)
	}
	if displayName := pdfBuyerBlockString(buyerBlock, "display_name"); displayName != "" {
		pdf.Cell(0, 6, displayName)
		pdf.Ln(6)
	}
	if legalName := pdfBuyerBlockString(buyerBlock, "legal_name"); legalName != "" {
		pdf.Cell(0, 6, legalName)
		pdf.Ln(6)
	}
	pdf.Ln(4)
}

// writeOfferPDFLineItems writes the line-items section. Each line's
// displayed unit price is its own persisted snapshot (offer_lines.go);
// this function shows it verbatim, deriving nothing.
func writeOfferPDFLineItems(pdf *fpdf.Fpdf, lines []crmcontracts.OfferLineItem, currency string, labels pdfLabels) {
	pdf.SetFont("Helvetica", "B", 12)
	pdf.Cell(0, 6, labels.lineItems)
	pdf.Ln(7)
	pdf.SetFont("Helvetica", "", 10)
	for _, li := range lines {
		pdf.Cell(0, 5, fmt.Sprintf("%d. %s", li.Position, li.Description))
		pdf.Ln(5)
		pdf.Cell(0, 5, fmt.Sprintf("%s x %s", pdfFormatQuantity(li.Quantity), pdfFormatMinor(li.UnitPriceMinor, currency)))
		pdf.Ln(5)
	}
	pdf.Ln(2)
}

// writeOfferPDFTotals writes the money summary straight off o's already-
// persisted Net/Tax/GrossMinor — the ONE totals figure this function (or
// any part of this renderer) ever shows; nothing here re-sums the lines.
func writeOfferPDFTotals(pdf *fpdf.Fpdf, o crmcontracts.Offer, labels pdfLabels) {
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
	pdf.Cell(0, 6, "Totals")
	pdf.Ln(7)
	pdf.SetFont("Helvetica", "", 11)
	pdf.Cell(0, 6, labels.net+": "+pdfFormatMinor(net, o.Currency))
	pdf.Ln(6)
	pdf.Cell(0, 6, labels.tax+": "+pdfFormatMinor(tax, o.Currency))
	pdf.Ln(6)
	pdf.Cell(0, 6, labels.gross+": "+pdfFormatMinor(gross, o.Currency))
	pdf.Ln(6)
}

// RenderOfferPDF builds the branded offer PDF from already-resolved
// inputs (see offer_render.go's PrepareRender): o carries the offer
// header and its server-computed totals, lines are its accepted line
// items, buyerBlock is the frozen buyer_snapshot once sent or the live
// buyer organization while still draft (nil when the offer has none),
// issuerName is the seller's display name, and locale drives the DE/EN
// label set. This function performs no database access and no money
// arithmetic — every figure it prints was computed and persisted before
// it was called (OFFER-AC-12a no-drift).
func RenderOfferPDF(o crmcontracts.Offer, lines []crmcontracts.OfferLineItem, buyerBlock map[string]any, issuerName, locale string) ([]byte, error) {
	labels := resolvePDFLabels(locale)
	number := ""
	if o.OfferNumber != nil {
		number = *o.OfferNumber
	}

	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetCompression(false)
	pdf.SetMargins(16, 16, 16)
	pdf.SetAutoPageBreak(true, 16)
	pdf.SetTitle(labels.title+" "+number, false)
	pdf.SetCreator("margince", false)
	pdf.SetAuthor(issuerName, false)
	pdf.SetSubject(labels.title+" PDF", false)
	pdf.AddPage()

	writeOfferPDFHeader(pdf, o, buyerBlock, issuerName, labels)
	writeOfferPDFLineItems(pdf, lines, o.Currency, labels)
	writeOfferPDFTotals(pdf, o, labels)

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, fmt.Errorf("render offer pdf: %w", err)
	}
	return buf.Bytes(), nil
}
