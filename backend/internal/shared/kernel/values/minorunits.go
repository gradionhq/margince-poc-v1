// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package values

// How many minor units a currency has, and how to say an amount out loud.
//
// Money is stored as an integer count of minor units, which is the only shape
// that does not lose cents. Rendering it is where the arithmetic goes wrong,
// and it goes wrong in two opposite directions: raw, and the reader takes
// 18000000 to mean eighteen million; divided by 100, and every zero-decimal
// currency is understated a hundredfold. VND, JPY and KRW have no minor unit at
// all — ¥18,000,000 IS eighteen million yen, and `/100` turns it into 180,000.
//
// It lives HERE, beside Money, because both callers of the table are on
// different sides of a dependency edge: the offer-draft price check is in
// package compose, and the account brief is in compose/orgbrief, which compose
// imports — so orgbrief cannot import compose and the table cannot live there.
// This package is Tier-0 and importable from both.

import (
	"fmt"
	"strconv"
	"strings"
)

// currencyMinorDigits lists the ISO-4217 codes whose minor unit is not the
// usual two digits — the zero-, three- and four-digit exceptions. Most
// currencies, including EUR and USD, carry two, so the table names the
// departures and the default below carries the rest.
//
// It is a list of the departures this build knows, NOT a claim to hold every
// one. ISO also assigns codes with no minor unit at all (the precious metals,
// XDR, the test code XTS), and the standard is amended. A code missing here
// renders at two digits and is wrong for that code — which is why the tolerable
// failure is the one MinorUnitDigits documents, and why adding a code is a
// one-line change rather than a redesign.
var currencyMinorDigits = map[string]int{
	"BIF": 0, "CLP": 0, "DJF": 0, "GNF": 0, "ISK": 0, "JPY": 0, "KMF": 0,
	"KRW": 0, "PYG": 0, "RWF": 0, "UGX": 0, "UYI": 0, "VND": 0, "VUV": 0,
	"XAF": 0, "XOF": 0, "XPF": 0,
	"BHD": 3, "IQD": 3, "JOD": 3, "KWD": 3, "LYD": 3, "OMR": 3, "TND": 3,
	// Four, and both are index units a contract may legitimately price in.
	"CLF": 4, "UYW": 4,
}

// MinorUnitDigits reports how many minor-unit digits a currency code carries.
//
// An unknown code answers 2 — the common case, and the only shape a code
// nobody has heard of can honestly be assumed to have. It is a guess, and it is
// the guess that is wrong least often; a caller that cannot afford a guess
// should not be rendering money it cannot identify.
func MinorUnitDigits(currency string) int {
	if digits, ok := currencyMinorDigits[strings.ToUpper(strings.TrimSpace(currency))]; ok {
		return digits
	}
	return 2
}

// MajorUnits renders an amount of minor units as the figure a person would say:
// "180000.00" for 18000000 EUR, "18000000" for the same integer in JPY.
//
// Fixed decimal places rather than a trimmed one, because the trailing zeroes
// are what say which unit this is. "180000" and "180000.00" read as the same
// number to a person and as two different claims to anything parsing them, and
// this figure's whole job is to stop a reader taking minor units for major
// ones.
//
// A negative amount keeps its sign on the front, where it belongs, rather than
// on whichever half the division happens to put it.
func MajorUnits(amountMinor int64, currency string) string {
	digits := MinorUnitDigits(currency)
	if digits == 0 {
		return strconv.FormatInt(amountMinor, 10)
	}
	scale := int64(1)
	for range digits {
		scale *= 10
	}
	// The magnitude is taken as UNSIGNED, because negating an int64 does not
	// always produce a positive one: math.MinInt64 has no positive counterpart
	// and negating it yields itself, which would print a minus sign in front of
	// a negative quotient. The API admits the whole int64 range, so the one
	// value that cannot be negated is a value it can be handed.
	// The wraparound IS the mechanism, not an oversight: uint64(x) for a
	// negative x wraps to x + 2^64, and negating that in unsigned arithmetic
	// yields |x| exactly — including for math.MinInt64, whose magnitude has no
	// int64 to hold it. That is the whole reason this does not simply negate.
	sign, magnitude := "", uint64(amountMinor) // #nosec G115 -- see above: the wrap is how |MinInt64| is obtained
	if amountMinor < 0 {
		sign, magnitude = "-", -uint64(amountMinor) // #nosec G115 -- same
	}
	// scale is 10^digits for digits in {2,3,4}, so it is small and positive.
	unsigned := uint64(scale) // #nosec G115 -- a power of ten bounded by the table above
	return fmt.Sprintf("%s%d.%0*d", sign, magnitude/unsigned, digits, magnitude%unsigned)
}
