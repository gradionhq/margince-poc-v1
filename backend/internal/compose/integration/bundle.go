// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

import (
	"archive/zip"
	"bytes"
	"encoding/csv"
	"io"
	"testing"
)

// Readers for the export bundle: a zip of CSVs. Exported because the suites that
// assert on a bundle's contents sit on both sides of the overlay split — the
// export suites here, the mirror-flip suite in the overlay package — and both
// want the same parse rather than two that could disagree about a header row.

// BundleEntries reads the produced ZIP into name→bytes.
//
// A repeated member name is a failure, not a last-one-wins overwrite: zip
// permits duplicates, and a map would silently keep only the final copy — so a
// bundle that shipped two conflicting overlay_mirror.csv entries would satisfy
// every assertion against whichever came last.
func BundleEntries(t *testing.T, raw []byte) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("opening bundle zip: %v", err)
	}
	entries := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("opening %s: %v", f.Name, err)
		}
		data, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("reading %s: %v", f.Name, err)
		}
		if err := rc.Close(); err != nil {
			t.Fatalf("closing %s: %v", f.Name, err)
		}
		if _, dup := entries[f.Name]; dup {
			t.Fatalf("the bundle carries %s twice — assertions would have read only the second copy", f.Name)
		}
		entries[f.Name] = data
	}
	return entries
}

// CSVColumn parses a CSV entry and returns the values under one column —
// the format-validity check (csv.Reader fails loudly on a malformed file)
// doubling as the content probe.
func CSVColumn(t *testing.T, raw []byte, column string) []string {
	t.Helper()
	records, err := csv.NewReader(bytes.NewReader(raw)).ReadAll()
	if err != nil {
		t.Fatalf("parsing csv: %v", err)
	}
	if len(records) == 0 {
		t.Fatal("csv has no header row")
	}
	idx := -1
	for i, h := range records[0] {
		if h == column {
			idx = i
		}
	}
	if idx == -1 {
		t.Fatalf("csv has no %q column; header=%v", column, records[0])
	}
	var out []string
	for _, row := range records[1:] {
		out = append(out, row[idx])
	}
	return out
}
