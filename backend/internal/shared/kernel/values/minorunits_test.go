// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package values

// Rendering money is where the arithmetic goes wrong, and it goes wrong in both
// directions: raw, and 18000000 reads as eighteen million; divided by 100, and
// every zero-decimal currency is understated a hundredfold.

import (
	"math"
	"testing"
)

func TestMajorUnitsRendersEachCurrencyInItsOwnScale(t *testing.T) {
	for _, tc := range []struct{ name, currency, want string }{
		{"two digits is the common case", "EUR", "180000.00"},
		{"an unknown code is guessed as two", "ZZZ", "180000.00"},
		{"a zero-decimal currency has no minor unit at all", "JPY", "18000000"},
		{"three digits", "KWD", "18000.000"},
		// Four-digit codes exist and were missing from the table: CLF is an
		// index unit a Chilean contract may legitimately be priced in, and
		// defaulting it to two digits overstates it a hundredfold.
		{"four digits", "CLF", "1800.0000"},
		{"lower case and padding are the same code", " eur ", "180000.00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := MajorUnits(18_000_000, tc.currency); got != tc.want {
				t.Errorf("MajorUnits(18000000, %q) = %s, want %s", tc.currency, got, tc.want)
			}
		})
	}
}

// The sign belongs on the front of the figure, not on whichever half the
// division happens to put it.
func TestMajorUnitsKeepsANegativeAmountReadable(t *testing.T) {
	for _, tc := range []struct {
		name     string
		minor    int64
		currency string
		want     string
	}{
		{"an ordinary negative", -18_000_000, "EUR", "-180000.00"},
		{"a negative under one major unit — the sign must not land on the whole part alone", -5, "EUR", "-0.05"},
		{"zero is a figure, not an absence", 0, "EUR", "0.00"},
		{"the largest amount the column admits", math.MaxInt64, "EUR", "92233720368547758.07"},
		// math.MinInt64 has no positive counterpart, so negating it yields
		// itself: a rendering that negates rather than taking the magnitude
		// unsigned prints a minus sign in front of an already-negative
		// quotient. The API admits the whole int64 range.
		{"the smallest amount the column admits", math.MinInt64, "EUR", "-92233720368547758.08"},
		{"the smallest, with no minor unit", math.MinInt64, "JPY", "-9223372036854775808"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := MajorUnits(tc.minor, tc.currency); got != tc.want {
				t.Errorf("MajorUnits(%d, %s) = %s, want %s", tc.minor, tc.currency, got, tc.want)
			}
		})
	}
}
