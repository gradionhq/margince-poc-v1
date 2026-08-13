// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package migration

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
)

// The profile's shape, and why it is not just a header list (IEM-WIRE-8): a
// mapping is a claim about columns, and the claim cannot be made from names
// alone. A column named "Region" that is 2% filled and one that is 98% filled
// are different decisions, and the only way to see that before committing is
// to be shown the rate and a few of the values.
const (
	// profileSamples is how many values one column shows. Three is enough to
	// recognize your own data and few enough that the response stays a hint
	// rather than a preview of the file.
	profileSamples = 3
	// ProfileRowLimit bounds one profiling read. The profile is a sample and
	// says so (Profile.RowsProfiled) — reading the whole file to compute an
	// exact fill rate would double the cost of an upload to sharpen a number
	// nobody decides differently on.
	ProfileRowLimit = 500
)

var (
	// ErrSourceUnreadable reports a file the CSV reader cannot parse — a
	// ragged row, an unterminated quote. The uploader gets told which line.
	ErrSourceUnreadable = errors.New("import source is unreadable")
	// ErrHeaderInvalid reports a header row that cannot address columns
	// unambiguously: absent, blank-named, or carrying the same name twice.
	// Two columns cannot both own one target, and picking one silently drops
	// the other's data.
	ErrHeaderInvalid = errors.New("import source header is unusable")
)

// Column is one column of an uploaded file, described well enough to map it
// without opening the file somewhere else.
type Column struct {
	Header   string
	Samples  []string
	FillRate float64
}

// Profile is what one uploaded file looks like: its columns, and how many rows
// that description was drawn from.
type Profile struct {
	Columns      []Column
	RowsProfiled int
}

// ProfileCSV reads at most rowLimit data rows and describes the columns it
// found. It never writes and never decides anything — the mapping is the
// human's, and this is the evidence they make it on.
func ProfileCSV(r io.Reader, rowLimit int) (Profile, error) {
	cr := csv.NewReader(r)
	cr.TrimLeadingSpace = true

	header, err := cr.Read()
	if errors.Is(err, io.EOF) {
		return Profile{}, fmt.Errorf("%w: the file has no header row", ErrHeaderInvalid)
	}
	if err != nil {
		return Profile{}, fmt.Errorf("%w: %v", ErrSourceUnreadable, err)
	}
	if err := validateHeader(header); err != nil {
		return Profile{}, err
	}

	filled := make([]int, len(header))
	samples := make([][]string, len(header))
	rows := 0
	for rows < rowLimit {
		record, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Profile{}, fmt.Errorf("%w: %v", ErrSourceUnreadable, err)
		}
		rows++
		for i, value := range record {
			if strings.TrimSpace(value) == "" {
				continue
			}
			filled[i]++
			if len(samples[i]) < profileSamples {
				samples[i] = append(samples[i], value)
			}
		}
	}

	columns := make([]Column, len(header))
	for i, name := range header {
		columns[i] = Column{Header: name, Samples: samples[i], FillRate: fillRate(filled[i], rows)}
	}
	return Profile{Columns: columns, RowsProfiled: rows}, nil
}

// fillRate is the share of profiled rows carrying a value. A file with no data
// rows reports 0 rather than dividing by zero: "nothing was read" and "nothing
// was filled" are the same answer to the only question the rate is asked.
func fillRate(filled, rows int) float64 {
	if rows == 0 {
		return 0
	}
	return float64(filled) / float64(rows)
}

func validateHeader(header []string) error {
	if len(header) == 0 {
		return fmt.Errorf("%w: the file has no header row", ErrHeaderInvalid)
	}
	seen := make(map[string]bool, len(header))
	for i, name := range header {
		trimmed := strings.TrimSpace(name)
		if trimmed == "" {
			return fmt.Errorf("%w: column %d has no name", ErrHeaderInvalid, i+1)
		}
		if seen[trimmed] {
			return fmt.Errorf("%w: the name %q appears twice", ErrHeaderInvalid, trimmed)
		}
		seen[trimmed] = true
	}
	return nil
}

// SuggestMapping proposes {source column → target field} for the columns whose
// normalized name equals a target's exactly. Nothing else is proposed.
//
// The timidity is the design. A screen shows a suggestion as a filled-in
// destination, so a wrong one is a mistake the human must first NOTICE and
// then undo, while a missing one is a blank they simply fill. "Company" does
// not become organization_id here, and neither does anything else that needs a
// guess about meaning rather than a comparison of names.
func SuggestMapping(p Profile, targets []string) map[string]string {
	byNormal := make(map[string]string, len(targets))
	for _, target := range targets {
		byNormal[normalizeFieldName(target)] = target
	}

	// A source name reaching a target more than once is ambiguous, and picking
	// either would silently drop the loser's column. Count first, decide after.
	hits := make(map[string]int, len(p.Columns))
	for _, c := range p.Columns {
		if _, ok := byNormal[normalizeFieldName(c.Header)]; ok {
			hits[normalizeFieldName(c.Header)]++
		}
	}

	out := make(map[string]string)
	for _, c := range p.Columns {
		normal := normalizeFieldName(c.Header)
		target, ok := byNormal[normal]
		if !ok || hits[normal] > 1 {
			continue
		}
		out[c.Header] = target
	}
	return out
}

// normalizeFieldName reduces a name to the letters and digits in it, folded to
// lower case, so "E-mail Address", "email_address" and "  Email address  " are
// one name. Punctuation and spacing are how humans and systems spell the same
// field differently; anything beyond that is a guess about meaning.
func normalizeFieldName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}
