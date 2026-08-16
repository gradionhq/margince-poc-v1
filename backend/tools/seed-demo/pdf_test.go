// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"strings"
	"testing"
)

// TestAccentsFoldRatherThanDot — the page is Helvetica with no embedded font,
// so a diacritic has no glyph. Folding it to the base letter keeps the line
// readable; dotting it produces "Th.i h.n", which looks like a broken file.
//
// This matters for names the copy cannot control: a Vietnamese company's
// legal name goes on its contract exactly as the crawl found it.
func TestAccentsFoldRatherThanDot(t *testing.T) {
	for input, want := range map[string]string{
		"Thời hạn":              "Thoi han",
		"Công ty Cổ phần":       "Cong ty Co phan",
		"Nguyễn Thị Mai Linh":   "Nguyen Thi Mai Linh",
		"Geschäftsführer":       "Geschaeftsfuehrer", // the German rule still wins
		"Trần Quốc Bảo":         "Tran Quoc Bao",
		"Đại Việt":              "Dai Viet",
		"plain ascii unchanged": "plain ascii unchanged",
	} {
		if got := escapePDFText(input); got != want {
			t.Errorf("escapePDFText(%q) = %q, want %q", input, got, want)
		}
	}
}

// TestUnrenderableRunesBecomeDots — a CJK character has no ASCII form, so
// there is nothing honest to fold it to.
func TestUnrenderableRunesBecomeDots(t *testing.T) {
	got := escapePDFText("株式会社")
	if got != "...." {
		t.Errorf("escapePDFText(CJK) = %q, want four dots", got)
	}
}

// TestPDFStringLiteralStaysBalanced — an unescaped paren or backslash ends
// the string literal early and turns the rest of the page into syntax.
func TestPDFStringLiteralStaysBalanced(t *testing.T) {
	got := escapePDFText(`a (b) c \ d`)
	if !strings.Contains(got, `\(`) || !strings.Contains(got, `\)`) || !strings.Contains(got, `\\`) {
		t.Errorf("escapePDFText did not escape the literal-breaking characters: %q", got)
	}
}

// TestRenderedPDFIsWellFormed — a document library whose entries fail to open
// demonstrates a broken product rather than a stocked one.
func TestRenderedPDFIsWellFormed(t *testing.T) {
	out := string(renderPDF(pdfPage{
		Title: "Hợp đồng khung",
		Lines: []string{"So hop dong: V-1234", "Thoi han: 2026-01-01 den 2026-12-31"},
	}))
	for _, marker := range []string{"%PDF-1.4", "xref", "trailer", "startxref", "%%EOF"} {
		if !strings.Contains(out, marker) {
			t.Errorf("rendered PDF has no %q", marker)
		}
	}
	if strings.Contains(out, "Hợp") {
		t.Error("the title reached the file unfolded — the base font cannot draw it")
	}
	if !strings.Contains(out, "Hop dong khung") {
		t.Error("the folded title is missing from the content stream")
	}
}
