// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package search

import (
	"strings"
	"testing"
	"time"
)

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

func TestMeasureQueryComputesNearestRankPercentiles(t *testing.T) {
	// 100 samples: 1ms..100ms. Nearest-rank: p50=50ms, p95=95ms, p99=99ms.
	runs := make([]time.Duration, 0, 100)
	for i := 100; i >= 1; i-- {
		runs = append(runs, ms(i))
	}
	stats, err := MeasureQuery("search_fts", Perf3Budget, runs)
	if err != nil {
		t.Fatal(err)
	}
	if stats.P50 != ms(50) || stats.P95 != ms(95) || stats.P99 != ms(99) {
		t.Fatalf("percentiles wrong: %+v", stats)
	}
	if stats.Samples != 100 {
		t.Fatalf("sample count wrong: %+v", stats)
	}
}

func TestMeasureQueryRefusesAnEmptyRun(t *testing.T) {
	if _, err := MeasureQuery("search_fts", Perf3Budget, nil); err == nil {
		t.Fatal("a query with no samples must be an error, not a silent pass")
	}
}

// The gate is real, not advisory: a seeded breach turns it red
// (B-EP05.21a acceptance).
func TestGateRedsOnASeededBreach(t *testing.T) {
	over, err := MeasureQuery(GraphQueryName, Perf7Budget, []time.Duration{ms(400), ms(410), ms(420)})
	if err != nil {
		t.Fatal(err)
	}
	under, err := MeasureQuery("search_fts", Perf3Budget, []time.Duration{ms(20), ms(30)})
	if err != nil {
		t.Fatal(err)
	}
	report := BenchReport{Tier: BenchTierSMB, Queries: []QueryStats{under, over}}
	gateErr := report.Gate()
	if gateErr == nil {
		t.Fatal("a p95 over budget must fail the gate")
	}
	if !strings.Contains(gateErr.Error(), GraphQueryName) || !strings.Contains(gateErr.Error(), "300ms") {
		t.Fatalf("breach error must name the query and budget: %v", gateErr)
	}

	if err := (BenchReport{Tier: BenchTierSMB, Queries: []QueryStats{under}}).Gate(); err != nil {
		t.Fatalf("an in-budget run must pass: %v", err)
	}
}

func TestTriggerEvidenceFiresAtMidMarketBreach(t *testing.T) {
	over, err := MeasureQuery(GraphQueryName, Perf7Budget, []time.Duration{ms(400)})
	if err != nil {
		t.Fatal(err)
	}
	ev := BenchReport{Tier: BenchTierMidMarket, Queries: []QueryStats{over}}.TriggerEvidence()
	if !ev.Triggered || !strings.Contains(ev.Trigger, "mid-market") {
		t.Fatalf("mid-market p95 breach must fire the named ADR-0021 trigger: %+v", ev)
	}
	if !strings.Contains(ev.String(), "TRIGGER") {
		t.Fatalf("evidence line must carry the verdict: %s", ev)
	}
}

// A breach on a small tier is a red gate but NOT a graph-store trigger:
// the ADR-0021 SLO binds at mid-market volume.
func TestTriggerEvidenceHoldsBelowMidMarket(t *testing.T) {
	over, err := MeasureQuery(GraphQueryName, Perf7Budget, []time.Duration{ms(400)})
	if err != nil {
		t.Fatal(err)
	}
	ev := BenchReport{Tier: BenchTierSMB, Queries: []QueryStats{over}}.TriggerEvidence()
	if ev.Triggered {
		t.Fatalf("an SMB-tier breach is not a mid-market trigger: %+v", ev)
	}
}

// Edge volume past an ADR-0021 threshold with p95 over budget is its
// own named trigger, tier-independent.
func TestTriggerEvidenceFiresOnEdgeVolume(t *testing.T) {
	over, err := MeasureQuery(GraphQueryName, Perf7Budget, []time.Duration{ms(400)})
	if err != nil {
		t.Fatal(err)
	}
	ev := BenchReport{
		Tier: BenchTierSMB, Queries: []QueryStats{over},
		RelationshipEdges: 6_000_000,
	}.TriggerEvidence()
	if !ev.Triggered || !strings.Contains(ev.Trigger, "5M") {
		t.Fatalf("edge-volume breach must fire the named trigger: %+v", ev)
	}

	inBudget, err := MeasureQuery(GraphQueryName, Perf7Budget, []time.Duration{ms(100)})
	if err != nil {
		t.Fatal(err)
	}
	ev = BenchReport{
		Tier: BenchTierSMB, Queries: []QueryStats{inBudget},
		RelationshipEdges: 6_000_000,
	}.TriggerEvidence()
	if ev.Triggered {
		t.Fatalf("edge volume alone (p95 in budget) is not a trigger: %+v", ev)
	}
}

// Every published budget is a STRICT bound — "< 200 ms", "< 300 ms". A p95
// landing exactly on the number does not satisfy it, and this is the boundary
// the gate got wrong: `>` let an exactly-at-budget run through as a pass while
// the mobile spec's toBeLessThan and the published page both called it a
// breach. One bound with three readers has to have one answer.
func TestAP95ExactlyOnTheBudgetIsABreachBecauseTheBoundIsStrict(t *testing.T) {
	onTheNumber, err := MeasureQuery(GraphQueryName, Perf7Budget, []time.Duration{Perf7Budget})
	if err != nil {
		t.Fatal(err)
	}
	report := BenchReport{Tier: BenchTierMidMarket, Queries: []QueryStats{onTheNumber}}
	if err := report.Gate(); err == nil {
		t.Fatalf("a p95 of exactly %s must breach the %s budget — the published bound is `< %s`, not `<=`",
			onTheNumber.P95, onTheNumber.Budget, onTheNumber.Budget)
	}

	// The neighbouring value still passes, so the tightening moved the boundary
	// by one nanosecond rather than shifting the whole gate.
	justUnder, err := MeasureQuery(GraphQueryName, Perf7Budget, []time.Duration{Perf7Budget - 1})
	if err != nil {
		t.Fatal(err)
	}
	nearMiss := BenchReport{Tier: BenchTierMidMarket, Queries: []QueryStats{justUnder}}
	if err := nearMiss.Gate(); err != nil {
		t.Fatalf("a p95 one nanosecond under the budget must pass: %v", err)
	}
}
