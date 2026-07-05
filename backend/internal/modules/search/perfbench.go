// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package search

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// The PERF budgets this module gates (06-nonfunctional §6.1). They are
// calibration starting values per ADR-0021 §5 — changing one is an
// ADR-noted budget revision, never a silent bump.
const (
	// Perf3Budget bounds ranked full-text search (PERF-3).
	Perf3Budget = 200 * time.Millisecond
	// Perf7Budget bounds context-graph assembly p95 at the mid-market
	// tier (PERF-7) — the graph-store trigger threshold (ADR-0021 §4).
	Perf7Budget = 300 * time.Millisecond
)

// ADR-0021 §4 edge-volume thresholds: crossing one with recursive-CTE
// p95 over budget is a named graph-store trigger.
const (
	triggerRelationshipEdges = 5_000_000
	triggerActivityLinkEdges = 50_000_000
)

// BenchTier names a §6.7 volume tier the harness runs against. PERF-7's
// SLO binds at mid-market; smaller tiers run the same gate as canaries.
type BenchTier string

const (
	BenchTierSMB       BenchTier = "smb"        // ~10k contacts
	BenchTierMidMarket BenchTier = "mid_market" // 250k–1M contacts
)

// QueryStats is the recorded distribution of one canonical query.
type QueryStats struct {
	Query   string // canonical query name, e.g. "search_fts", "context_graph"
	Budget  time.Duration
	P50     time.Duration
	P95     time.Duration
	P99     time.Duration
	Samples int
}

// MeasureQuery folds raw run durations into the percentile record the
// gate reads. Percentiles use the nearest-rank method, so every
// reported value is a latency that actually happened.
func MeasureQuery(name string, budget time.Duration, runs []time.Duration) (QueryStats, error) {
	if len(runs) == 0 {
		return QueryStats{}, fmt.Errorf("search: perfbench: query %s recorded no samples", name)
	}
	sorted := append([]time.Duration(nil), runs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	rank := func(q float64) time.Duration {
		idx := int(float64(len(sorted))*q+0.999999) - 1
		if idx < 0 {
			idx = 0
		}
		if idx >= len(sorted) {
			idx = len(sorted) - 1
		}
		return sorted[idx]
	}
	return QueryStats{
		Query: name, Budget: budget,
		P50: rank(0.50), P95: rank(0.95), P99: rank(0.99),
		Samples: len(sorted),
	}, nil
}

// BenchReport is one harness run over one seeded tier: the recorded
// canonical-query distributions plus the workspace edge counts the
// ADR-0021 trigger reads.
type BenchReport struct {
	Tier              BenchTier
	Queries           []QueryStats
	RelationshipEdges int64
	ActivityLinkEdges int64
}

// Gate is the CI verdict: any canonical query whose p95 breaches its
// budget fails the build. The error names every breach so a red run
// says what regressed, not just that something did.
func (r BenchReport) Gate() error {
	var breaches []string
	for _, q := range r.Queries {
		if q.P95 > q.Budget {
			breaches = append(breaches,
				fmt.Sprintf("%s p95 %s over the %s budget (tier %s, %d samples)",
					q.Query, q.P95, q.Budget, r.Tier, q.Samples))
		}
	}
	if len(breaches) > 0 {
		return fmt.Errorf("search: perfbench: %s", strings.Join(breaches, "; "))
	}
	return nil
}

// TriggerEvidence is the ADR-0021 graph-store trigger record a harness
// run emits: whether a named trigger condition fired, and the measured
// facts it fired (or held) on. A passing run confirms the relational +
// pgvector substrate; a breach — after the tuning ladder is exhausted —
// is the evidence an ADR revisit cites.
type TriggerEvidence struct {
	Tier              BenchTier
	GraphAssemblyP95  time.Duration
	Budget            time.Duration
	RelationshipEdges int64
	ActivityLinkEdges int64
	Triggered         bool
	Trigger           string // the named ADR-0021 §4 trigger; empty when none fired
}

// GraphQueryName is the canonical-query id the trigger evidence reads
// its p95 from — the context-graph assembly PERF-7 measures.
const GraphQueryName = "context_graph"

// TriggerEvidence computes the trigger record from this run.
func (r BenchReport) TriggerEvidence() TriggerEvidence {
	ev := TriggerEvidence{
		Tier: r.Tier, Budget: Perf7Budget,
		RelationshipEdges: r.RelationshipEdges,
		ActivityLinkEdges: r.ActivityLinkEdges,
	}
	for _, q := range r.Queries {
		if q.Query == GraphQueryName {
			ev.GraphAssemblyP95 = q.P95
		}
	}
	overBudget := ev.GraphAssemblyP95 > Perf7Budget
	switch {
	case overBudget && r.Tier == BenchTierMidMarket:
		ev.Triggered = true
		ev.Trigger = "PERF-7 p95 over 300ms at mid-market"
	case overBudget && r.RelationshipEdges > triggerRelationshipEdges:
		ev.Triggered = true
		ev.Trigger = "relationship edges past 5M with recursive-CTE p95 over budget"
	case overBudget && r.ActivityLinkEdges > triggerActivityLinkEdges:
		ev.Triggered = true
		ev.Trigger = "activity_link edges past 50M with recursive-CTE p95 over budget"
	}
	return ev
}

// String renders the evidence line a CI log keeps as the trigger
// record over time (06-nonfunctional §6.1: results tracked so slow
// drift is caught).
func (ev TriggerEvidence) String() string {
	verdict := "substrate confirmed"
	if ev.Triggered {
		verdict = "TRIGGER: " + ev.Trigger
	}
	return fmt.Sprintf("ADR-0021 graph-store trigger [%s tier]: graph-assembly p95 %s / budget %s, %d relationship edges, %d activity_link edges — %s",
		ev.Tier, ev.GraphAssemblyP95, ev.Budget, ev.RelationshipEdges, ev.ActivityLinkEdges, verdict)
}
