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
	"math"
	"math/big"
	"strconv"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
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

// FieldFault names the numeric field that would not parse as a decimal.
func (e *DecimalFieldError) FieldFault() (field, code, message string) {
	return e.Field, "invalid_decimal", e.Error()
}

// MoneyRangeError maps to 422: a derived figure larger than the integer
// minor units every money column and contract field is declared in
// (bigint / format:int64). The engine refuses instead of narrowing,
// because big.Int.Int64() on an out-of-range value yields the low 64
// bits — a plausible, frequently negative number the offer row, the PDF
// and the pipeline rollup all go on to treat as real money.
//
// It states the INSTALLATION's limit and does not instruct the caller to
// shrink anything. The ceiling is not a small number in any currency this
// money model serves — a zero-decimal currency spends it one minor unit
// at a time (values.MinorUnitDigits: VND, JPY, KRW are 0), which still
// leaves a single line room for several times world GDP — so a figure
// that reaches it is either an overflow probe or an amount this model
// genuinely cannot hold. Telling the second caller to lower their amount
// would be telling them to falsify it.
type MoneyRangeError struct {
	Figure string   // the derived value that left the range (line_net_minor, gross_minor, …)
	Fields []string // the contract inputs that feed the figure, so the caller can locate it
}

func (e *MoneyRangeError) Error() string {
	return e.Figure + " is larger than this installation stores exactly (integer minor units, at most " +
		strconv.FormatInt(math.MaxInt64, 10) + ")"
}

// FieldFaults names every input that feeds the figure — which inputs
// combined to produce it, not which one is wrong. The figure is derived,
// so no single input is at fault, and a refusal that named just one would
// point at an arbitrary member of the set.
func (e *MoneyRangeError) FieldFaults() []apperrors.FieldRefusal {
	refusals := make([]apperrors.FieldRefusal, 0, len(e.Fields))
	for _, field := range e.Fields {
		refusals = append(refusals, apperrors.FieldRefusal{
			Field: field, Code: "money_not_representable", Message: e.Error(),
		})
	}
	return refusals
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
	netMinor, err := minorFromBig(roundHalfUp(net), "line_net_minor", "quantity", "unit_price_minor")
	if err != nil {
		return LineFigures{}, err
	}

	tax := new(big.Rat).SetInt64(netMinor)
	tax.Mul(tax, taxRate)
	tax.Quo(tax, hundred)
	taxMinor, err := minorFromBig(roundHalfUp(tax), "line_tax_minor", "quantity", "unit_price_minor", "tax_rate")
	if err != nil {
		return LineFigures{}, err
	}

	total := new(big.Int).Add(big.NewInt(netMinor), big.NewInt(taxMinor))
	totalMinor, err := minorFromBig(total, "line_total_minor", "quantity", "unit_price_minor")
	if err != nil {
		return LineFigures{}, err
	}
	return LineFigures{NetMinor: netMinor, TaxMinor: taxMinor, TotalMinor: totalMinor}, nil
}

// statefulOfferLine pairs a line's money inputs with its proposal state
// so the totals derivation can honor the staged/accepted split.
type statefulOfferLine struct {
	Line  OfferLineInput
	State ProposalState
}

// acceptedLines narrows an offer's lines to the ones that count toward
// its totals: a staged line is an unaccepted AI proposal (E03.21a) and
// must never move a number the buyer can see.
func acceptedLines(lines []statefulOfferLine) []OfferLineInput {
	out := make([]OfferLineInput, 0, len(lines))
	for _, l := range lines {
		if l.State == ProposalAccepted {
			out = append(out, l.Line)
		}
	}
	return out
}

// OfferTotals sums the per-line figures: net/tax/gross are Σ over lines,
// so the stored totals reconcile to the displayed lines with zero drift.
// The sums accumulate in exact big integers because lines that each fit
// int64 can still add past it — a native += would wrap on the offer row
// even though every line it reconciles to reads correctly.
func OfferTotals(lines []OfferLineInput) (OfferFigures, error) {
	net, tax, gross := new(big.Int), new(big.Int), new(big.Int)
	for i, line := range lines {
		fig, err := LineTotals(line)
		if err != nil {
			return OfferFigures{}, fmt.Errorf("line %d: %w", i+1, err)
		}
		net.Add(net, big.NewInt(fig.NetMinor))
		tax.Add(tax, big.NewInt(fig.TaxMinor))
		gross.Add(gross, big.NewInt(fig.TotalMinor))
	}

	var out OfferFigures
	var err error
	if out.NetMinor, err = minorFromBig(net, "net_minor", "line_items"); err != nil {
		return OfferFigures{}, err
	}
	if out.TaxMinor, err = minorFromBig(tax, "tax_minor", "line_items"); err != nil {
		return OfferFigures{}, err
	}
	if out.GrossMinor, err = minorFromBig(gross, "gross_minor", "line_items"); err != nil {
		return OfferFigures{}, err
	}
	return out, nil
}

// minorFromBig narrows an exact figure to the integer minor units the
// money columns hold, or refuses naming the inputs that produced it.
// Every derived value in this engine passes through here: it is the ONE
// place that decides an unrepresentable total is a refusal rather than a
// wrapped number.
func minorFromBig(v *big.Int, figure string, fields ...string) (int64, error) {
	if !v.IsInt64() {
		return 0, &MoneyRangeError{Figure: figure, Fields: fields}
	}
	return v.Int64(), nil
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
//
// The result stays an exact big integer: quantity × unit_price_minor is
// unbounded by either column, so narrowing belongs to minorFromBig,
// which refuses what int64 cannot hold instead of wrapping it.
func roundHalfUp(x *big.Rat) *big.Int {
	num := new(big.Int).Set(x.Num())
	den := x.Denom() // always > 0 for big.Rat
	twice := num.Mul(num, big.NewInt(2))
	if twice.Sign() >= 0 {
		twice.Add(twice, den)
	} else {
		twice.Sub(twice, den)
	}
	// Quo truncates toward zero; for negatives, floor(x+1/2) semantics
	// mirror to ceil(x−1/2), which truncation already yields here.
	return new(big.Int).Quo(twice, new(big.Int).Mul(den, big.NewInt(2)))
}

// formatQuantity renders the contract's float64 quantity at the DB's
// numeric(14,3) scale — the one conversion point from wire number to
// exact decimal, so the engine and the column always agree.
func formatQuantity(v float64) string { return strconv.FormatFloat(v, 'f', 3, 64) }

// formatPct renders a percentage at numeric(5,2) scale.
func formatPct(v float64) string { return strconv.FormatFloat(v, 'f', 2, 64) }
