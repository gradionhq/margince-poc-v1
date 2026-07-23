// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package values

import "strings"

// PlainDecimal reports whether s is a plain non-negative decimal — digits
// with at most one dot, within maxInt integer and maxFrac fractional digits.
// It rejects the rational ("1/3") and scientific ("1e3") forms big.Rat also
// accepts, so callers that feed s to big.Rat can send every rejection down a
// clean validation path. One spelling shared by the rate stores (fx_rate and
// ai_model_rate) that both parse decimal strings before scaling to integers.
func PlainDecimal(s string, maxInt, maxFrac int) bool {
	if s == "" {
		return false
	}
	intPart, fracPart, hasDot := strings.Cut(s, ".")
	if intPart == "" || len(intPart) > maxInt || !allDigits(intPart) {
		return false
	}
	if hasDot && (fracPart == "" || len(fracPart) > maxFrac || !allDigits(fracPart)) {
		return false
	}
	return true
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
