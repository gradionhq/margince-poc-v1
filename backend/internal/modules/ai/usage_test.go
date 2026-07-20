// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package ai

import (
	"testing"
	"time"
)

func TestBudgetBandWalksTheThresholds(t *testing.T) {
	const budget = 1_000
	cases := []struct {
		name  string
		spent int64
		want  string
	}{
		{"fresh month", 0, BandNormal},
		{"just under the degrade line", 799, BandNormal},
		{"at 80% the soft-degrade starts", 800, BandDegraded},
		{"just under the cap", 999, BandDegraded},
		{"at 100% non-interactive queues", 1_000, BandQueued},
		{"over the cap stays queued", 2_500, BandQueued},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := budgetBand(tc.spent, budget); got != tc.want {
				t.Fatalf("budgetBand(%d, %d) = %q, want %q", tc.spent, budget, got, tc.want)
			}
		})
	}
}

func TestBudgetBandFailsClosedOnNonPositiveBudget(t *testing.T) {
	if got := budgetBand(0, 0); got != BandQueued {
		t.Fatalf("a zero budget must read as exhausted, got %q", got)
	}
}

func TestUsageWindowDefaultsToTheCurrentMonth(t *testing.T) {
	m := &Meter{now: func() time.Time {
		return time.Date(2026, time.July, 17, 15, 4, 5, 0, time.UTC)
	}}
	from, to := m.UsageWindow()
	if want := time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC); !from.Equal(want) {
		t.Fatalf("from = %v, want the first of the month %v", from, want)
	}
	if to.Format(time.DateOnly) != "2026-07-17" {
		t.Fatalf("to = %v, want today", to)
	}
}

func TestWireAiUsageCarriesEveryCounterAndTheBand(t *testing.T) {
	day := time.Date(2026, time.July, 16, 0, 0, 0, 0, time.UTC)
	days := []DayUsage{{
		Day: day,
		Tasks: []TaskUsage{
			{
				Task: "capture_classify", Tier: "local_small", Calls: 4, CachedHits: 1, TokensIn: 900, TokensOut: 120,
				CostEstMicroUSD: 32_500, UnpricedCalls: 0,
			},
			{Task: "enrich", Tier: "cheap_cloud", Calls: 2, TokensIn: 300, TokensOut: 80},
		},
	}}
	wire := wireAiUsage(days, BudgetStatus{MonthlyTokens: 12_000_000, SpentTokens: 1_400, Band: BandNormal})

	if len(wire.Days) != 1 || len(wire.Days[0].Tasks) != 2 {
		t.Fatalf("wire shape = %d days / %d tasks, want 1/2", len(wire.Days), len(wire.Days[0].Tasks))
	}
	if wire.Days[0].Date.Format(time.DateOnly) != "2026-07-16" {
		t.Fatalf("date = %v, want the aggregate day", wire.Days[0].Date)
	}
	first := wire.Days[0].Tasks[0]
	if first.Task != "capture_classify" || first.Tier != "local_small" ||
		first.Calls != 4 || *first.CachedHits != 1 || first.TokensIn != 900 || first.TokensOut != 120 {
		t.Fatalf("first task line dropped a counter: %+v", first)
	}
	// A priced rate (32_500 micro-USD = 3 cents, truncating) reports its
	// cost even though this line's UnpricedCalls is 0.
	if first.CostEstMinor == nil || *first.CostEstMinor != 3 {
		t.Fatalf("cost_est_minor = %v, want 3 (32_500 microUSD / 10_000)", first.CostEstMinor)
	}
	// The second line carries no cost data at all (CostEstMicroUSD and
	// UnpricedCalls both zero-value, the "genuinely free" reading, not
	// "entirely unpriced") — cost_est_minor reports the honest 0.
	second := wire.Days[0].Tasks[1]
	if second.CostEstMinor == nil || *second.CostEstMinor != 0 {
		t.Fatalf("cost_est_minor = %v, want 0 (zero cost, zero unpriced calls — genuinely free, not unpriced)", second.CostEstMinor)
	}
	if wire.Budget.MonthlyTokens != 12_000_000 || wire.Budget.SpentTokens != 1_400 ||
		string(wire.Budget.Band) != BandNormal {
		t.Fatalf("budget block mismatch: %+v", wire.Budget)
	}
	if wire.Budget.Currency == nil || *wire.Budget.Currency != "USD" {
		t.Fatalf("budget.currency = %v, want USD", wire.Budget.Currency)
	}
	if wire.Budget.BandSince != nil {
		t.Fatalf("band transitions are not tracked — band_since must be omitted")
	}
}

// TestWireAiUsageOmitsCostForAnEntirelyUnpricedTask proves the
// price-on-read honesty rule (ADR-0067, global constraint "cost is
// transparency, never a gate"): a task line whose window cost is 0
// because every one of its calls lacked a matching rate row — not
// because the calls were free — must omit cost_est_minor rather than
// report a fabricated 0.
func TestWireAiUsageOmitsCostForAnEntirelyUnpricedTask(t *testing.T) {
	days := []DayUsage{{
		Day: time.Date(2026, time.July, 16, 0, 0, 0, 0, time.UTC),
		Tasks: []TaskUsage{
			{
				Task: "summarize", Tier: "premium", Calls: 3, TokensIn: 500, TokensOut: 100,
				CostEstMicroUSD: 0, UnpricedCalls: 3,
			},
		},
	}}
	wire := wireAiUsage(days, BudgetStatus{})
	if got := wire.Days[0].Tasks[0].CostEstMinor; got != nil {
		t.Fatalf("cost_est_minor = %d, want omitted (nil) — every call in this task/day was unpriced", *got)
	}
}

// TestWireAiUsageReportsPartialCostWhenSomeCallsAreUnpriced proves the
// other half of the rule: a task line with a real, non-zero priced total
// reports it even when some of the same window's calls had no rate — a
// partial dollar figure is honest, a fabricated 0 is not.
func TestWireAiUsageReportsPartialCostWhenSomeCallsAreUnpriced(t *testing.T) {
	days := []DayUsage{{
		Day: time.Date(2026, time.July, 16, 0, 0, 0, 0, time.UTC),
		Tasks: []TaskUsage{
			{
				Task: "summarize", Tier: "premium", Calls: 3, TokensIn: 500, TokensOut: 100,
				CostEstMicroUSD: 50_000, UnpricedCalls: 1,
			},
		},
	}}
	wire := wireAiUsage(days, BudgetStatus{})
	got := wire.Days[0].Tasks[0].CostEstMinor
	if got == nil || *got != 5 {
		t.Fatalf("cost_est_minor = %v, want 5 (50_000 microUSD / 10_000) despite 1 unpriced call in the same line", got)
	}
}

// The aliased CachedHits pointer must be per-line, not the loop
// variable's address reused across lines.
func TestWireAiUsageCachedHitsAreNotAliased(t *testing.T) {
	days := []DayUsage{{
		Day: time.Date(2026, time.July, 16, 0, 0, 0, 0, time.UTC),
		Tasks: []TaskUsage{
			{Task: "a", Tier: "local_small", CachedHits: 1},
			{Task: "b", Tier: "local_small", CachedHits: 7},
		},
	}}
	wire := wireAiUsage(days, BudgetStatus{})
	if *wire.Days[0].Tasks[0].CachedHits != 1 || *wire.Days[0].Tasks[1].CachedHits != 7 {
		t.Fatalf("cached_hits aliased across lines: %d, %d",
			*wire.Days[0].Tasks[0].CachedHits, *wire.Days[0].Tasks[1].CachedHits)
	}
}
