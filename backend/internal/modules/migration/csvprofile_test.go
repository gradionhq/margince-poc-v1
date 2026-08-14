// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package migration

import (
	"errors"
	"strings"
	"testing"
)

func TestProfileCSVReadsHeadersSamplesAndFillRate(t *testing.T) {
	const file = "Company Name,Website,Notes\n" +
		"Acme,acme.test,\n" +
		"Globex,globex.test,\n" +
		"Initech,,\n" +
		"Umbrella,umbrella.test,keep\n"

	p, err := ProfileCSV(strings.NewReader(file), 100)
	if err != nil {
		t.Fatalf("ProfileCSV: %v", err)
	}
	if p.RowsProfiled != 4 {
		t.Fatalf("rows profiled = %d, want 4", p.RowsProfiled)
	}
	if len(p.Columns) != 3 {
		t.Fatalf("columns = %d, want 3", len(p.Columns))
	}
	if p.Columns[0].Header != "Company Name" {
		t.Errorf("header = %q, want the file's own spelling", p.Columns[0].Header)
	}
	// Three of four rows carry a website; the fill rate is what tells a human
	// their mapping is about to leave a quarter of the column empty.
	if got := p.Columns[1].FillRate; got != 0.75 {
		t.Errorf("website fill rate = %v, want 0.75", got)
	}
	if got := p.Columns[2].FillRate; got != 0.25 {
		t.Errorf("notes fill rate = %v, want 0.25", got)
	}
	// Samples are values a human recognizes, capped, and skip the blanks —
	// three empty strings would say nothing about the column.
	if want := []string{"acme.test", "globex.test", "umbrella.test"}; !equalStrings(p.Columns[1].Samples, want) {
		t.Errorf("website samples = %q, want %q", p.Columns[1].Samples, want)
	}
	if len(p.Columns[2].Samples) != 1 || p.Columns[2].Samples[0] != "keep" {
		t.Errorf("notes samples = %q, want just the one non-empty value", p.Columns[2].Samples)
	}
}

func TestProfileCSVCapsSamplesAtThree(t *testing.T) {
	file := "Email\na@x.test\nb@x.test\nc@x.test\nd@x.test\n"

	p, err := ProfileCSV(strings.NewReader(file), 100)
	if err != nil {
		t.Fatalf("ProfileCSV: %v", err)
	}
	if len(p.Columns[0].Samples) != 3 {
		t.Fatalf("samples = %d, want 3 — the profile is a hint, not a preview of the file", len(p.Columns[0].Samples))
	}
	if p.Columns[0].FillRate != 1 {
		t.Errorf("fill rate = %v, want 1: capping SAMPLES must not cap the count behind the rate", p.Columns[0].FillRate)
	}
}

// The row limit bounds the read, and RowsProfiled reports what was actually
// read — a fill rate quoted against a number of rows nobody looked at is a
// statistic about nothing.
func TestProfileCSVStopsAtTheRowLimitAndSaysSo(t *testing.T) {
	var b strings.Builder
	b.WriteString("Email\n")
	for i := range 50 {
		if i < 10 {
			b.WriteString("filled@x.test\n")
			continue
		}
		b.WriteString("\n")
	}

	p, err := ProfileCSV(strings.NewReader(b.String()), 10)
	if err != nil {
		t.Fatalf("ProfileCSV: %v", err)
	}
	if p.RowsProfiled != 10 {
		t.Fatalf("rows profiled = %d, want 10", p.RowsProfiled)
	}
	if p.Columns[0].FillRate != 1 {
		t.Errorf("fill rate = %v, want 1 over the rows actually read", p.Columns[0].FillRate)
	}
}

func TestProfileCSVRefusesAHeaderItCannotMapUnambiguously(t *testing.T) {
	for _, tc := range []struct {
		name string
		file string
		want error
	}{
		{"duplicate names", "Email,Email\na@x.test,b@x.test\n", ErrHeaderInvalid},
		{"blank name", "Email,,Website\na@x.test,x,y\n", ErrHeaderInvalid},
		{"no header at all", "", ErrHeaderInvalid},
		{"ragged row", "Email,Website\na@x.test\n", ErrSourceUnreadable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ProfileCSV(strings.NewReader(tc.file), 100)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestSuggestMappingMatchesNormalizedNamesOnly(t *testing.T) {
	p := Profile{Columns: []Column{
		{Header: "E-mail Address"},
		{Header: "  first_name  "},
		{Header: "Company"},
	}}
	targets := []string{"email_address", "first_name", "organization_id", "cf_region"}

	got := SuggestMapping(p, targets)

	if got["E-mail Address"] != "email_address" {
		t.Errorf("E-mail Address → %q, want email_address", got["E-mail Address"])
	}
	if got["  first_name  "] != "first_name" {
		t.Errorf("first_name → %q, want first_name", got["  first_name  "])
	}
	// "Company" is not "organization_id" by any rule this function is allowed
	// to apply. An absent suggestion is a blank the human fills; a wrong one is
	// a mistake they must first notice.
	if v, ok := got["Company"]; ok {
		t.Errorf("Company → %q, want no suggestion at all", v)
	}
}

func TestSuggestMappingRefusesAnAmbiguousMatch(t *testing.T) {
	// Two columns that normalize to the same target: suggesting either would
	// be picking one at random, and the loser's data silently never lands.
	p := Profile{Columns: []Column{{Header: "Email Address"}, {Header: "email-address"}}}

	got := SuggestMapping(p, []string{"email_address"})

	if len(got) != 0 {
		t.Fatalf("suggestions = %v, want none — the match is ambiguous", got)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
