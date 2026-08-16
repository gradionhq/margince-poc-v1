// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// A minimal PDF writer, so the demo's documents are real files rather than
// bytes with a .pdf name on them.
//
// It matters that these open. A document library whose entries fail to render
// demonstrates a broken product rather than a stocked one, and the extraction
// lane reads what is actually in the file — a placeholder would teach it that
// every contract is empty.
//
// Deliberately small: PDF 1.4, one page, Helvetica, no compression, no
// embedded fonts. That is enough for a readable page in every viewer and it
// keeps the whole generator inside one file somebody can check. Anything
// richer belongs to a real document service, not to a seeder.

import (
	"bytes"
	"fmt"
	"strings"
)

// pdfPage is what one generated document says.
type pdfPage struct {
	Title string
	Lines []string
}

// renderPDF lays the page out as a single-page PDF and returns the bytes.
//
// The structure is the four objects a PDF minimally needs — catalog, page
// tree, page, content — plus a font, and the cross-reference table that says
// where each begins. Offsets are counted as the file is built, because a
// byte-offset table that disagrees with the body is exactly what makes a
// reader refuse the file.
func renderPDF(page pdfPage) []byte {
	content := contentStream(page)
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] " +
			"/Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}

	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects))
	for i, body := range objects {
		offsets[i] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", i+1, body)
	}

	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, offset := range offsets {
		fmt.Fprintf(&out, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		len(objects)+1, xref)
	return out.Bytes()
}

// contentStream draws the title and the body lines down the page.
func contentStream(page pdfPage) string {
	const (
		leftMargin = 60
		topLine    = 780
		lineHeight = 18
	)
	var b strings.Builder
	b.WriteString("BT\n/F1 16 Tf\n")
	fmt.Fprintf(&b, "%d %d Td\n(%s) Tj\n", leftMargin, topLine, escapePDFText(page.Title))
	b.WriteString("/F1 11 Tf\n")
	for i, line := range page.Lines {
		// The first Td already moved to the title; each line steps down from
		// wherever the last one left the cursor.
		step := lineHeight
		if i == 0 {
			step = lineHeight * 2
		}
		fmt.Fprintf(&b, "0 -%d Td\n(%s) Tj\n", step, escapePDFText(line))
	}
	b.WriteString("ET")
	return b.String()
}

// escapePDFText makes one line safe inside a PDF string literal.
//
// Backslash and the two parens would end the literal early or unbalance it,
// which turns the rest of the page into syntax. Non-ASCII is transliterated
// rather than escaped: a one-page demo document in WinAnsi has no way to
// carry "Geschäftsführer" correctly, and a mojibake title is worse than an
// honest ASCII one.
func escapePDFText(s string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`, `(`, `\(`, `)`, `\)`,
		"ä", "ae", "ö", "oe", "ü", "ue",
		"Ä", "Ae", "Ö", "Oe", "Ü", "Ue", "ß", "ss",
		"—", "-", "–", "-", "’", "'", "„", `"`, "“", `"`,
	)
	escaped := replacer.Replace(s)
	// Anything still outside printable ASCII cannot be rendered by the base
	// font, so it becomes a dot rather than a broken glyph.
	var b strings.Builder
	for _, r := range escaped {
		if r < 32 || r > 126 {
			b.WriteRune('.')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
