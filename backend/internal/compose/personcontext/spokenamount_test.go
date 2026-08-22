// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package personcontext

import (
	"math"
	"strings"
	"testing"
)

// The defect this function exists to close: the three copies it replaced each
// divided by 100, so every currency with no minor unit read a hundredfold low —
// on a pre-meeting brief, a relationship brief and an OUTBOUND email draft.
//
// The zero-decimal rows are therefore the point of the table, not an edge case,
// and each states the figure a reader of that currency would recognise.
func TestSpokenAmount(t *testing.T) {
	for name, tc := range map[string]struct {
		amountMinor int64
		currency    string
		want        string
	}{
		"two-decimal currency loses its cents":            {9_500_000, "EUR", "€95k"},
		"under a thousand keeps its unit":                 {45_000, "USD", "$450"},
		"an unlisted code renders as the code":            {9_500_000, "CHF", "CHF 95k"},
		"a billion-euro deal stays exact to the thousand": {150_000_000_000, "EUR", "€1500000k"},
		"zero-decimal: VND is not divided":                {18_000_000, "VND", "₫18000k"},
		"zero-decimal: JPY is not divided":                {950_000, "JPY", "¥950k"},
		"three-decimal: KWD has a milli-unit":             {95_000_000, "KWD", "KWD 95k"},
		"no amount says nothing":                          {0, "EUR", ""},
		"no currency says nothing":                        {9_500_000, "", ""},
		"an amount below one unit rounds toward zero":     {50, "EUR", "€0"},
		// The case that removed the "m" tier: truncating to the million read
		// €1,999,999 as "€1m", understating by nearly half in an outbound
		// draft. To the thousand it loses 999 units at worst.
		"just under two million is not rounded to one": {199_999_900, "EUR", "€1999k"},
		// A deal amount can be negative: no CHECK on the column, no minimum in
		// the contract, and a POST carrying one answers 201. Comparing the
		// SIGNED value against the tiers abbreviated only the positive half and
		// put the minus inside the figure — "€-95000".
		"a credit is abbreviated too":            {-9_500_000, "EUR", "-€95k"},
		"a credit in the millions":               {-150_000_000_000, "EUR", "-€1500000k"},
		"a credit in a zero-decimal currency":    {-18_000_000, "VND", "-₫18000k"},
		"a small credit keeps its sign in front": {-45_000, "USD", "-$450"},
		// Under one major unit the figure truncates to zero, and a zero taken
		// as positive turned a refund into "€0" — nothing at all, which reads
		// as true where an ugly figure reads as odd.
		"a credit under one major unit is still a credit": {-50, "EUR", "-€0"},
		// The one magnitude int64 negation cannot express. Taking it unsigned
		// is what stops a minus sign printing in front of a negative quotient.
		"the most negative amount there is": {math.MinInt64, "EUR", "-€92233720368547k"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := SpokenAmount(tc.amountMinor, tc.currency); got != tc.want {
				t.Errorf("SpokenAmount(%d, %q) = %q, want %q", tc.amountMinor, tc.currency, got, tc.want)
			}
		})
	}
}

// The regression in one line: a currency with no minor unit must not be
// divided. Restoring `major := amountMinor / 100` anywhere under this call
// turns ₫100 into ₫1 and fails here, whatever the symbols and thresholds above
// have been rewritten to.
func TestAZeroDecimalCurrencyIsNeverDivided(t *testing.T) {
	for _, currency := range []string{"VND", "JPY", "KRW", "ISK", "XAF"} {
		digits := SpokenAmount(100, currency)
		if !strings.HasSuffix(digits, "100") {
			t.Errorf("SpokenAmount(100, %q) = %q, want the whole 100: %s has no minor unit, so the integer IS the amount",
				currency, digits, currency)
		}
	}
}
