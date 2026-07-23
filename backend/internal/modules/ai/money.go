// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"math/big"
	"strings"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/values"
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
func UsdPerMTokToMicroUSD(field, usd string) (int64, error) {
	s := strings.TrimSpace(usd)
	if !values.PlainDecimal(s, 13, 6) {
		return 0, rateInvalid(field, "rate_price_nonnegative",
			field+" must be a plain non-negative decimal (USD per 1M tokens, up to 6 fractional digits)")
	}
	r, _ := new(big.Rat).SetString(s)
	r.Mul(r, new(big.Rat).SetInt64(microUSDPerMTok))
	num, den := r.Num(), r.Denom()
	q := new(big.Int).Quo(num, den)
	// The 6-fractional-digit cap above makes num/den exact after scaling by 1e6,
	// so this round-half-up branch never fires today — it is a defensive guard
	// that keeps the conversion correct if that cap is ever widened.
	if new(big.Int).Mul(new(big.Int).Rem(num, den), big.NewInt(2)).CmpAbs(den) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	if !q.IsInt64() {
		return 0, rateInvalid(field, "rate_price_too_large", field+" is too large")
	}
	return q.Int64(), nil
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
