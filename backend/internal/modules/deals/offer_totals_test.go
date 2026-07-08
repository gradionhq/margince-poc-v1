// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

import (
	"fmt"
	"math/big"
	"testing"
)

// The B-E03.18 golden cases: line_net = round(qty × unit_price ×
// (1 − discount_pct/100)), line_tax = round(line_net × tax_rate/100),
// line_total = net + tax — integer minor units, half-up per line.
func TestLineTotalsGoldenCases(t *testing.T) {
	cases := []struct {
		name string
		line OfferLineInput
		want LineFigures
	}{
		{
			name: "unit price times one, DE VAT",
			line: OfferLineInput{Quantity: "1.000", UnitPriceMinor: 10000, DiscountPct: "0.00", TaxRate: "19.00"},
			want: LineFigures{NetMinor: 10000, TaxMinor: 1900, TotalMinor: 11900},
		},
		{
			name: "fractional quantity with discount and reduced rate",
			// 999 × 2.5 × 0.9 = 2247.75 → 2248; 2248 × 7% = 157.36 → 157
			line: OfferLineInput{Quantity: "2.500", UnitPriceMinor: 999, DiscountPct: "10.00", TaxRate: "7.00"},
			want: LineFigures{NetMinor: 2248, TaxMinor: 157, TotalMinor: 2405},
		},
		{
			name: "exact half rounds up, never banker's-down here",
			// 5 × 1 × 0.5 = 2.5 → 3
			line: OfferLineInput{Quantity: "1.000", UnitPriceMinor: 5, DiscountPct: "50.00", TaxRate: "0.00"},
			want: LineFigures{NetMinor: 3, TaxMinor: 0, TotalMinor: 3},
		},
		{
			name: "milli-quantity keeps integer money",
			// 1000 × 0.333 = 333
			line: OfferLineInput{Quantity: "0.333", UnitPriceMinor: 1000, DiscountPct: "0.00", TaxRate: "0.00"},
			want: LineFigures{NetMinor: 333, TaxMinor: 0, TotalMinor: 333},
		},
		{
			name: "full discount zeroes the line",
			line: OfferLineInput{Quantity: "4.000", UnitPriceMinor: 123456, DiscountPct: "100.00", TaxRate: "19.00"},
			want: LineFigures{NetMinor: 0, TaxMinor: 0, TotalMinor: 0},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := LineTotals(tc.line)
			if err != nil {
				t.Fatalf("LineTotals(%+v): %v", tc.line, err)
			}
			if got != tc.want {
				t.Fatalf("LineTotals(%+v) = %+v, want %+v", tc.line, got, tc.want)
			}
		})
	}
}

func TestLineTotalsRejectsMalformedDecimals(t *testing.T) {
	for _, bad := range []OfferLineInput{
		{Quantity: "2,5", UnitPriceMinor: 100, DiscountPct: "0", TaxRate: "0"},
		{Quantity: "1", UnitPriceMinor: 100, DiscountPct: "ten", TaxRate: "0"},
		{Quantity: "1", UnitPriceMinor: 100, DiscountPct: "0", TaxRate: "1e2"},
		{Quantity: "1/3", UnitPriceMinor: 100, DiscountPct: "0", TaxRate: "0"},
	} {
		if _, err := LineTotals(bad); err == nil {
			t.Errorf("LineTotals(%+v) accepted a malformed decimal", bad)
		}
	}
}

// The P11 reconciliation property (B-E03.18): 100 random offers' totals
// must equal an INDEPENDENTLY computed ground truth with zero drift, and
// the offer sums must equal the sum of the per-line figures exactly.
// The ground truth uses pure integer arithmetic over the scaled inputs
// (qty in thousandths, percentages in hundredths) — a different
// formulation than the engine's rational path.
func TestOfferTotalsReconcileToGroundTruth(t *testing.T) {
	rng := &splitMix{state: 42} // deterministic: a failure names its offer
	for i := 0; i < 100; i++ {
		lineCount := 1 + rng.intn(12)
		lines := make([]OfferLineInput, lineCount)
		var wantNet, wantTax, wantGross int64
		for j := range lines {
			qtyMilli := 1 + rng.intn(5_000_000) // 0.001 – 5000.000
			price := rng.intn(50_000_000)       // 0 – 500k major units
			discountCenti := rng.intn(10_001)   // 0.00 – 100.00 %
			taxCenti := rng.intn(3_001)         // 0.00 – 30.00 %
			lines[j] = OfferLineInput{
				Quantity:       fmt.Sprintf("%d.%03d", qtyMilli/1000, qtyMilli%1000),
				UnitPriceMinor: price,
				DiscountPct:    fmt.Sprintf("%d.%02d", discountCenti/100, discountCenti%100),
				TaxRate:        fmt.Sprintf("%d.%02d", taxCenti/100, taxCenti%100),
			}
			net := roundedIntDiv(
				new(big.Int).Mul(big.NewInt(price), new(big.Int).Mul(big.NewInt(qtyMilli), big.NewInt(10_000-discountCenti))),
				big.NewInt(10_000_000)) // 1000 (milli) × 10000 (centi-pct)
			tax := roundedIntDiv(new(big.Int).Mul(big.NewInt(net), big.NewInt(taxCenti)), big.NewInt(10_000))
			wantNet += net
			wantTax += tax
			wantGross += net + tax
		}

		got, err := OfferTotals(lines)
		if err != nil {
			t.Fatalf("offer %d: %v", i, err)
		}
		if got.NetMinor != wantNet || got.TaxMinor != wantTax || got.GrossMinor != wantGross {
			t.Fatalf("offer %d drifted from ground truth: got %+v, want net=%d tax=%d gross=%d\nlines: %+v",
				i, got, wantNet, wantTax, wantGross, lines)
		}

		// The stored sums must also equal the sum of the DISPLAYED lines.
		var sumNet, sumTax, sumTotal int64
		for _, line := range lines {
			fig, err := LineTotals(line)
			if err != nil {
				t.Fatalf("offer %d: %v", i, err)
			}
			sumNet += fig.NetMinor
			sumTax += fig.TaxMinor
			sumTotal += fig.TotalMinor
		}
		if got.NetMinor != sumNet || got.TaxMinor != sumTax || got.GrossMinor != sumTotal {
			t.Fatalf("offer %d: totals do not reconcile to their own lines: %+v vs Σ(net=%d tax=%d total=%d)",
				i, got, sumNet, sumTax, sumTotal)
		}
	}
}

// splitMix is a tiny deterministic SplitMix64 so the property inputs are
// reproducible from the seed without a crypto-vs-math rand debate — the
// generator's quality only needs to cover the input space, not security.
type splitMix struct{ state uint64 }

func (s *splitMix) intn(n int64) int64 {
	s.state += 0x9e3779b97f4a7c15
	z := s.state
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	z ^= z >> 31
	return int64(z % uint64(n)) //nolint:gosec // n is a small positive test bound
}

// roundedIntDiv is the test's own half-up integer division: (2n + d) / 2d
// floored, for non-negative n.
func roundedIntDiv(n, d *big.Int) int64 {
	twice := new(big.Int).Mul(n, big.NewInt(2))
	twice.Add(twice, d)
	twice.Div(twice, new(big.Int).Mul(d, big.NewInt(2)))
	return twice.Int64()
}

// E03.21a: staged (AI-proposed, not yet accepted) lines never move the
// server-computed offer totals — only accepted lines feed the engine.
func TestAcceptedLinesExcludeStagedFromTotals(t *testing.T) {
	accepted := statefulOfferLine{
		Line:  OfferLineInput{Quantity: "1.000", UnitPriceMinor: 10000, DiscountPct: "0.00", TaxRate: "19.00"},
		State: ProposalAccepted,
	}
	staged := statefulOfferLine{
		Line:  OfferLineInput{Quantity: "3.000", UnitPriceMinor: 50000, DiscountPct: "0.00", TaxRate: "19.00"},
		State: ProposalStaged,
	}
	acceptedOnly := OfferFigures{NetMinor: 10000, TaxMinor: 1900, GrossMinor: 11900}

	cases := []struct {
		name  string
		lines []statefulOfferLine
		want  OfferFigures
	}{
		{name: "no lines totals to zero", lines: nil, want: OfferFigures{}},
		{name: "accepted line counts", lines: []statefulOfferLine{accepted}, want: acceptedOnly},
		{name: "staged line alone contributes nothing", lines: []statefulOfferLine{staged}, want: OfferFigures{}},
		{
			name:  "staged beside accepted leaves totals untouched",
			lines: []statefulOfferLine{accepted, staged},
			want:  acceptedOnly,
		},
		{
			name:  "accepting the staged line flips it into the totals",
			lines: []statefulOfferLine{accepted, {Line: staged.Line, State: ProposalAccepted}},
			want:  OfferFigures{NetMinor: 160000, TaxMinor: 30400, GrossMinor: 190400},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := OfferTotals(acceptedLines(tc.lines))
			if err != nil {
				t.Fatalf("OfferTotals: %v", err)
			}
			if got != tc.want {
				t.Fatalf("totals = %+v, want %+v", got, tc.want)
			}
		})
	}
}
