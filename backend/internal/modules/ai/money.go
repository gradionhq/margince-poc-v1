// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"math/big"
	"strings"
)

// RateValidationError is the ai module's typed 422 for a rejected model-rate
// write; the rate handlers map it to httperr.Validation on the wire.
type RateValidationError struct {
	Field   string
	Code    string
	Message string
}

func (e *RateValidationError) Error() string { return e.Message }

func rateInvalid(field, code, message string) error {
	return &RateValidationError{Field: field, Code: code, Message: message}
}

const microUSDPerMTok = 1_000_000

// UsdPerMTokToMicroUSD converts a USD-per-million-tokens decimal string
// (e.g. "5.00") into the µUSD/MTok integer the ai_model_rate table stores
// ("5.00" -> 5_000_000). It rejects a non-plain-decimal (the rational "1/3"
// and scientific "1e3" forms big.Rat also accepts), negative, or too-large
// value (exceeding int64 after scaling). Rounds half-up at µUSD.
func UsdPerMTokToMicroUSD(usd string) (int64, error) {
	s := strings.TrimSpace(usd)
	if !plainDecimal(s, 13, 6) {
		return 0, rateInvalid("price", "rate_price_nonnegative",
			"price must be a plain non-negative decimal (USD per 1M tokens, up to 6 fractional digits)")
	}
	r, _ := new(big.Rat).SetString(s)
	r.Mul(r, new(big.Rat).SetInt64(microUSDPerMTok))
	num, den := r.Num(), r.Denom()
	q := new(big.Int).Quo(num, den)
	if new(big.Int).Mul(new(big.Int).Rem(num, den), big.NewInt(2)).CmpAbs(den) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	if !q.IsInt64() {
		return 0, rateInvalid("price", "rate_price_too_large", "price is too large")
	}
	return q.Int64(), nil
}

// plainDecimal answers whether s is a plain non-negative decimal — digits
// with at most one dot, within maxInt integer and maxFrac fractional digits.
// It rejects the rational ("1/3") and scientific ("1e3") forms big.Rat also
// accepts, so every rejection lands on the clean 422 path.
func plainDecimal(s string, maxInt, maxFrac int) bool {
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

// MicroUSDToUsdPerMTok formats a stored µUSD/MTok integer back to a trimmed
// USD-per-million-tokens decimal string (5_000_000 -> "5", 150_000 -> "0.15").
func MicroUSDToUsdPerMTok(micro int64) string {
	s := new(big.Rat).SetFrac(big.NewInt(micro), big.NewInt(microUSDPerMTok)).FloatString(6)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(s, "0")
		s = strings.TrimRight(s, ".")
	}
	return s
}
