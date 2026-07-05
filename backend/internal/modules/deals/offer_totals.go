// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// The deterministic offer money-totals engine (B-E03.18, data-model
// §12.6): line and offer totals are DERIVED here — server-side, integer
// minor units, exact rational arithmetic — and nowhere else. Inputs are
// decimal STRINGS (the DB's ::text rendering of numeric columns), so no
// float ever touches money (P11); rounding is half-up, applied per line,
// so the offer totals reconcile exactly to the sum of displayed lines.

package deals

import (
	"fmt"
	"math/big"
	"strconv"
)

// OfferLineInput is one line as the engine consumes it: exact decimal
// strings for the numeric(…) columns plus the integer price snapshot.
type OfferLineInput struct {
	Quantity       string // numeric(14,3), > 0
	UnitPriceMinor int64
	DiscountPct    string // numeric(5,2), 0–100
	TaxRate        string // numeric(5,2), ≥ 0
}

// LineFigures are one line's derived money values, in minor units.
type LineFigures struct {
	NetMinor   int64
	TaxMinor   int64
	TotalMinor int64
}

// OfferFigures are the offer-level sums over its lines.
type OfferFigures struct {
	NetMinor   int64
	TaxMinor   int64
	GrossMinor int64
}

// DecimalFieldError maps to 422: a quantity/percentage that is not a
// plain decimal number.
type DecimalFieldError struct{ Field, Value string }

func (e *DecimalFieldError) Error() string {
	return e.Field + " must be a decimal number, got " + strconv.Quote(e.Value)
}

// LineTotals derives one line's figures (formulas: line_net =
// round(qty × unit_price × (1 − discount_pct/100)), line_tax =
// round(line_net × tax_rate/100), line_total = line_net + line_tax).
func LineTotals(line OfferLineInput) (LineFigures, error) {
	qty, err := ratFromDecimal("quantity", line.Quantity)
	if err != nil {
		return LineFigures{}, err
	}
	discount, err := ratFromDecimal("discount_pct", line.DiscountPct)
	if err != nil {
		return LineFigures{}, err
	}
	taxRate, err := ratFromDecimal("tax_rate", line.TaxRate)
	if err != nil {
		return LineFigures{}, err
	}

	hundred := big.NewRat(100, 1)
	keep := new(big.Rat).Sub(hundred, discount) // 100 − discount_pct
	net := new(big.Rat).SetInt64(line.UnitPriceMinor)
	net.Mul(net, qty)
	net.Mul(net, keep)
	net.Quo(net, hundred)
	netMinor := roundHalfUp(net)

	tax := new(big.Rat).SetInt64(netMinor)
	tax.Mul(tax, taxRate)
	tax.Quo(tax, hundred)
	taxMinor := roundHalfUp(tax)

	return LineFigures{NetMinor: netMinor, TaxMinor: taxMinor, TotalMinor: netMinor + taxMinor}, nil
}

// OfferTotals sums the per-line figures: net/tax/gross are Σ over lines,
// so the stored totals reconcile to the displayed lines with zero drift.
func OfferTotals(lines []OfferLineInput) (OfferFigures, error) {
	var out OfferFigures
	for i, line := range lines {
		fig, err := LineTotals(line)
		if err != nil {
			return OfferFigures{}, fmt.Errorf("line %d: %w", i+1, err)
		}
		out.NetMinor += fig.NetMinor
		out.TaxMinor += fig.TaxMinor
		out.GrossMinor += fig.TotalMinor
	}
	return out, nil
}

// ratFromDecimal parses a plain decimal string exactly. big.Rat's
// SetString also accepts fractions ("1/3") and exponents; the DB never
// renders those, and accepting them here would widen the engine's input
// language beyond the numeric columns it mirrors.
func ratFromDecimal(field, value string) (*big.Rat, error) {
	for _, r := range value {
		if (r < '0' || r > '9') && r != '.' && r != '-' {
			return nil, &DecimalFieldError{Field: field, Value: value}
		}
	}
	rat, ok := new(big.Rat).SetString(value)
	if !ok {
		return nil, &DecimalFieldError{Field: field, Value: value}
	}
	return rat, nil
}

// roundHalfUp rounds a non-negative rational to the nearest integer,
// ties away from zero — the one rounding the whole engine uses, so the
// displayed sum equals the sum of displayed values (AC-R11-style
// reconciliation). Line inputs are constrained non-negative (quantity
// > 0, price ≥ 0, discount ≤ 100, tax ≥ 0 — DB CHECKs), so the
// negative branch cannot arise; it is still handled symmetrically
// rather than silently misrounding a future caller.
func roundHalfUp(x *big.Rat) int64 {
	num := new(big.Int).Set(x.Num())
	den := x.Denom() // always > 0 for big.Rat
	twice := num.Mul(num, big.NewInt(2))
	if twice.Sign() >= 0 {
		twice.Add(twice, den)
	} else {
		twice.Sub(twice, den)
	}
	q := new(big.Int).Quo(twice, new(big.Int).Mul(den, big.NewInt(2)))
	// Quo truncates toward zero; for negatives, floor(x+1/2) semantics
	// mirror to ceil(x−1/2), which truncation already yields here.
	return q.Int64()
}

// formatQuantity renders the contract's float64 quantity at the DB's
// numeric(14,3) scale — the one conversion point from wire number to
// exact decimal, so the engine and the column always agree.
func formatQuantity(v float64) string { return strconv.FormatFloat(v, 'f', 3, 64) }

// formatPct renders a percentage at numeric(5,2) scale.
func formatPct(v float64) string { return strconv.FormatFloat(v, 'f', 2, 64) }
