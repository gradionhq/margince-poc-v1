// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package personcontext

import (
	"fmt"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
)

// currencySymbols is the short form a reader recognises without being told
// which currency it is. It is deliberately SHORT: a symbol nobody reads at a
// glance is worse than the code, because it says the same thing in a shape the
// reader has to decode. Anything not listed renders as its ISO code, which is
// unambiguous everywhere.
var currencySymbols = map[string]string{
	"EUR": "€",
	"USD": "$",
	"GBP": "£",
	"VND": "₫",
	"JPY": "¥",
}

// SpokenAmount renders a deal's value the way somebody would SAY it in a
// sentence: "€95k", not "95000.00 EUR". The exact figure belongs on the deal
// card, where a reader is checking a number; here it is one clause of a
// sentence, and the full decimal spelling reads as a database field pasted into
// prose.
//
// A zero amount and an amount with no currency are both rendered as nothing: a
// figure whose scale the reader has to guess is worse in an outbound message
// than no figure at all.
//
// It lives here, and the pre-meeting brief, the relationship brief and the
// person draft all call it, because all three used to hold a BYTE-IDENTICAL
// copy that divided by 100 — and every zero-decimal currency was understated a
// hundredfold on all three at once, one of them an outbound email. The scale
// comes from values.WholeMajorUnits, which reads the ISO minor-unit table, so a
// currency corrected there is corrected on every surface.
func SpokenAmount(amountMinor int64, currency string) string {
	if amountMinor == 0 || currency == "" {
		return ""
	}
	symbol, named := currencySymbols[currency]
	if !named {
		symbol = currency + " "
	}
	// The tier is chosen from the MAGNITUDE and the sign put back in front,
	// because comparing a signed value against the thresholds abbreviates only
	// the positive half: -9 500 000 EUR is not >= 1000, so it came out as the
	// unabbreviated "€-95000" — the exact shape this function exists to keep
	// out of a sentence, with the minus sign in the middle of the figure. A
	// deal amount CAN be negative: the column carries no non-negative CHECK and
	// the contract sets no minimum, so a credit reaches a brief and an outbound
	// draft like any other figure.
	//
	// Unsigned, and taken by the same wrap values.MajorUnits documents:
	// negating math.MinInt64 as an int64 yields itself, so a magnitude taken
	// that way would print a minus in front of a negative number.
	// The sign comes from the AMOUNT, not from the truncated major figure. A
	// credit smaller than one major unit truncates to zero, and a zero taken as
	// positive rendered -50 EUR as "€0" — a refund read as nothing at all,
	// which is worse than an ugly figure because it is a plausible one.
	major := values.WholeMajorUnits(amountMinor, currency)
	sign, magnitude := "", uint64(major) // #nosec G115 -- the wrap IS how |MinInt64| is obtained; see above
	if amountMinor < 0 {
		sign, magnitude = "-", -uint64(major) // #nosec G115 -- same
	}
	// One abbreviation step, and deliberately not two. A currency with no minor
	// unit states ordinary sums in the millions, so "₫18000k" is coarser than a
	// reader would like — but an "m" tier truncating the same way understates
	// ₫1,999,999 as "₫1m", nearly by half, in an outbound email. Truncating to
	// the thousand loses at most 999 units; truncating to the million loses up
	// to a million. The uglier figure is the honest one, and this figure's whole
	// job is to be trusted at a glance.
	if magnitude >= 1000 {
		return fmt.Sprintf("%s%s%dk", sign, symbol, magnitude/1000)
	}
	return fmt.Sprintf("%s%s%d", sign, symbol, magnitude)
}
