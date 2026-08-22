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
		// ISO assigns no minor unit to the metals, so the integer IS the
		// amount and two digits would invent a scale the code does not have.
		{"a code with no minor unit at all", "XAU", "18000000"},
		// The ordinary currencies are NOT in the table — it holds the
		// exceptions — so this is the answer for most of the world's money,
		// and it must not become a refusal.
		{"an ordinary currency the table does not name", "GBP", "180000.00"},
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

// WholeMajorUnits is the integer half of the same table, for the surfaces that
// SAY an amount rather than state it. It is tested here, in the package that
// owns it, and not only through its caller: an exported Tier-0 function whose
// only coverage comes from another package is one a future caller can break
// without any test in this package noticing.
func TestWholeMajorUnitsTruncatesAtTheCurrencysOwnScale(t *testing.T) {
	for name, tc := range map[string]struct {
		amountMinor int64
		currency    string
		want        int64
	}{
		"two decimals is the ordinary case":       {18_000_000, "EUR", 180_000},
		"VND has no minor unit, so it is itself":  {18_000_000, "VND", 18_000_000},
		"JPY likewise":                            {950_000, "JPY", 950_000},
		"KWD carries three":                       {95_000, "KWD", 95},
		"CLF carries four":                        {1_234_567, "CLF", 123},
		"an unknown code takes the ISO default":   {12_345, "ZZZ", 123},
		"below one major unit truncates to zero":  {50, "EUR", 0},
		"and truncates toward zero when negative": {-50, "EUR", 0},
		"a credit keeps its sign":                 {-18_000_000, "EUR", -180_000},
	} {
		t.Run(name, func(t *testing.T) {
			if got := WholeMajorUnits(tc.amountMinor, tc.currency); got != tc.want {
				t.Errorf("WholeMajorUnits(%d, %q) = %d, want %d", tc.amountMinor, tc.currency, got, tc.want)
			}
		})
	}
}

// The one value int64 negation cannot express. Dividing it is safe — the
// divisor is a small positive power of ten — and the result is what a caller
// taking the magnitude has to be able to handle without printing a minus in
// front of a negative number.
func TestWholeMajorUnitsSurvivesTheMostNegativeAmount(t *testing.T) {
	if got, want := WholeMajorUnits(math.MinInt64, "EUR"), int64(math.MinInt64/100); got != want {
		t.Errorf("WholeMajorUnits(MinInt64, EUR) = %d, want %d", got, want)
	}
	if got, want := WholeMajorUnits(math.MinInt64, "VND"), int64(math.MinInt64); got != want {
		t.Errorf("WholeMajorUnits(MinInt64, VND) = %d, want %d — a zero-decimal currency divides by one", got, want)
	}
}
