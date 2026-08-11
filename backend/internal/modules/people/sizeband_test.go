// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package people

import "testing"

func TestSizeBandFromEmployeeRangeMapsOnlyUnambiguousPhrasings(t *testing.T) {
	cases := []struct {
		text string
		band string
		ok   bool
	}{
		// Enum-shaped values pass through as themselves.
		{"51-200", "51-200", true},
		{"5000+", "5000+", true},
		// A stated range maps when both ends share one band.
		{"25 to 50", "11-50", true},
		{"11-50", "11-50", true},
		{"60–180", "51-200", true},
		// A single stated headcount maps to its containing band.
		{"about 120 people", "51-200", true},
		{"ca. 40 Mitarbeiter", "11-50", true},
		{"7", "1-10", true},
		{"1,200 employees", "1001-5000", true},
		{"1.200 Mitarbeitende", "1001-5000", true},
		// A floor with no ceiling fits only the open top band.
		{"6000+", "5000+", true},
		{"200+", "", false},
		{"50+ employees", "", false},
		// A range spanning two bands is nobody's clean answer.
		{"50-200", "", false},
		{"10 to 500", "", false},
		// An inverted range is a parse artifact, not a statement.
		{"200 to 50", "", false},
		// Magnitude words mean the digits are not the headcount.
		{"10k employees", "", false},
		{"2 thousand", "", false},
		{"about 1 million users", "", false},
		// Decimals, zeros, and numberless prose all abstain.
		{"2.5", "", false},
		{"0 employees", "", false},
		{"a growing team", "", false},
		{"", "", false},
		// Three numbers state no single range.
		{"between 10, 50 and 200", "", false},
		// A comparison states a bound, not a placeable range.
		{">500 employees", "", false},
		{"over 500", "", false},
		{"up to 50", "", false},
		{"mehr als 1000", "", false},
		{"über 200 Mitarbeiter", "", false},
		// Magnitude shorthand in any spelling refuses.
		{"2m employees", "", false},
		{"10-k", "", false},
		{"2 Tsd. Mitarbeiter", "", false},
		// Register identifiers and negatives are not headcounts.
		{"HRB 9001", "", false},
		{"-5 employees", "", false},
		// Mixed separators are no unambiguous integer.
		{"1,234.567", "", false},
		// The band edges, both sides.
		{"10", "1-10", true},
		{"11", "11-50", true},
		{"200", "51-200", true},
		{"201", "201-500", true},
		{"5000", "1001-5000", true},
		{"5001", "5000+", true},
	}
	for _, c := range cases {
		band, ok := sizeBandFromEmployeeRange(c.text)
		if band != c.band || ok != c.ok {
			t.Errorf("sizeBandFromEmployeeRange(%q) = (%q, %v), want (%q, %v)", c.text, band, ok, c.band, c.ok)
		}
	}
}
