// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The deals-by-stage report's weighted figure (AC-F1): the per-stage
// weighted total must equal the sum of each constituent deal's OWN rounded
// weighted value, not the stage's probability applied once to the summed
// raw total — the same invariant the forecast report already proves for
// itself in report_forecast_integration_test.go. The reports screen's
// deals-by-stage table reads a server aggregate with no per-deal rows to
// round client-side, so the engine has to compute it the AC-F1 way.

import (
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

func TestDealsByStageWeightedReconcilesToPerDealRounding(t *testing.T) {
	e := setupForecast(t)

	// Two equal deals whose OWN weighted values each round UP (12341×60% =
	// 7404.6 → 7405), while their combined raw total rounds DOWN
	// (24682×60% = 14809.2 → 14809): round(Σamount×p/100) and
	// Σround(amount×p/100) disagree by exactly 1 for this stage.
	const dealAmount = int64(12341)
	e.seedOpenDeal(t, "Alpha", 60, nil, int64p(dealAmount), stringp("commit"))
	e.seedOpenDeal(t, "Beta", 60, nil, int64p(dealAmount), stringp("commit"))
	// A different stage's deal must not fold into the group under test.
	e.seedOpenDeal(t, "Elsewhere", 20, nil, int64p(999999), stringp("commit"))

	result := e.runReport(t, e.Admin(), "deals-by-stage",
		`{"group_by":["stage_id"],"aggregates":[{"fn":"count","as":"deals"},{"fn":"sum","field":"amount_minor","as":"amount_minor_sum"},{"fn":"sum","field":"weighted_amount_minor","as":"weighted_minor"}]}`)
	row := dealsByStageRow(t, result, e.stages[60].String())

	wantWeighted := weightedMinor(dealAmount, 60) + weightedMinor(dealAmount, 60)
	sumFirstWeighted := weightedMinor(2*dealAmount, 60) // round(Σamount × p / 100) — the rejected methodology
	if wantWeighted == sumFirstWeighted {
		t.Fatal("fixture is broken: the two methodologies agree, so this test cannot discriminate between them")
	}
	if got := wireInt(t, row, "weighted_minor"); got != wantWeighted {
		if got == sumFirstWeighted {
			t.Fatalf("weighted_minor = %d (rounded the column sum once), want %d (Σround(amount×p/100) — rounded per deal, then summed)", got, wantWeighted)
		}
		t.Fatalf("weighted_minor = %d, want %d", got, wantWeighted)
	}
	if got := wireInt(t, row, "amount_minor_sum"); got != 2*dealAmount {
		t.Errorf("amount_minor_sum = %d, want %d", got, 2*dealAmount)
	}
}

// "Explain This Number" must resolve the new measure too: its drill-through
// source rows carry weighted_amount_minor and reconcile exactly to the
// displayed weighted_minor, the same AC-F1 guarantee the forecast report
// proves for itself in TestForecastDerivationDrillThroughReconcilesExactly.
func TestDealsByStageWeightedDerivationReconcilesExactly(t *testing.T) {
	e := setupForecast(t)
	e.seedOpenDeal(t, "Alpha", 60, nil, int64p(12341), stringp("commit"))
	e.seedOpenDeal(t, "Beta", 60, nil, int64p(12341), stringp("commit"))
	e.seedOpenDeal(t, "Elsewhere", 20, nil, int64p(999999), stringp("commit"))

	result := e.runReport(t, e.Admin(), "deals-by-stage",
		`{"group_by":["stage_id"],"aggregates":[{"fn":"count","as":"deals"},{"fn":"sum","field":"amount_minor","as":"amount_minor_sum"},{"fn":"sum","field":"weighted_amount_minor","as":"weighted_minor"}]}`)
	row := dealsByStageRow(t, result, e.stages[60].String())
	handle, ok := row["derivation_url"].(string)
	if !ok || handle == "" {
		t.Fatalf("aggregate row has no derivation_url: %+v", row)
	}

	derivation := e.explainReport(t, e.Admin(), "deals-by-stage", handle)
	if len(derivation.Rows) != 2 || derivation.TotalRows != 2 {
		t.Fatalf("drill-through = %d rows (total %d), want the stage's 2 deals: %+v",
			len(derivation.Rows), derivation.TotalRows, derivation.Rows)
	}
	var weighted int64
	for _, source := range derivation.Rows {
		amount := wireInt(t, source, "amount_minor")
		probability := wireInt(t, source, "win_probability")
		rowWeighted := wireInt(t, source, "weighted_amount_minor")
		if rowWeighted != weightedMinor(amount, probability) {
			t.Errorf("source row weighted = %d, want round(%d × %d%%) = %d",
				rowWeighted, amount, probability, weightedMinor(amount, probability))
		}
		weighted += rowWeighted
	}
	if weighted != wireInt(t, row, "weighted_minor") {
		t.Errorf("drill-through weighted sum %d != displayed %d", weighted, wireInt(t, row, "weighted_minor"))
	}
}

// The stage join must not widen what a team-scoped rep's drill-through sees:
// the same row-scope clause the forecast suite proves in
// TestForecastDerivationHonorsRowScope applies here too, and a foreign
// owner's deal in the SAME stage must stay invisible.
func TestDealsByStageDerivationHonorsRowScope(t *testing.T) {
	e := setupForecast(t)
	e.seedOpenDeal(t, "Mine", 60, &e.Rep1, int64p(10000), stringp("commit"))
	e.seedOpenDeal(t, "Theirs", 60, &e.Rep3, int64p(999999), stringp("commit"))

	rep := e.dealReadCtx(e.Rep1, nil, principal.RowScopeOwn)
	result := e.runReport(t, rep, "deals-by-stage",
		`{"group_by":["stage_id"],"aggregates":[{"fn":"count","as":"deals"},{"fn":"sum","field":"amount_minor","as":"amount_minor_sum"},{"fn":"sum","field":"weighted_amount_minor","as":"weighted_minor"}]}`)
	row := dealsByStageRow(t, result, e.stages[60].String())
	if got := wireInt(t, row, "deals"); got != 1 {
		t.Fatalf("deals = %d, want 1 — the foreign rep's deal in the same stage must not be counted", got)
	}
	if got := wireInt(t, row, "weighted_minor"); got != weightedMinor(10000, 60) {
		t.Errorf("weighted_minor = %d, want %d (only the caller's own deal)", got, weightedMinor(10000, 60))
	}

	handle, ok := row["derivation_url"].(string)
	if !ok || handle == "" {
		t.Fatalf("aggregate row has no derivation_url: %+v", row)
	}
	derivation := e.explainReport(t, rep, "deals-by-stage", handle)
	if derivation.TotalRows != 1 {
		t.Errorf("own-scope drill-through total = %d, want 1 (never the foreign deal)", derivation.TotalRows)
	}
}

// dealsByStageRow picks the aggregate row for one stage out of a
// group-by-stage_id result — the report is fetched with no stage_id
// filter (there is no caller for one; grouping already answers "per
// stage"), so the test selects its row the way TestForecastByOwnerCounts…
// selects by owner_id.
func dealsByStageRow(t *testing.T, result reportResultWire, stageID string) map[string]any {
	t.Helper()
	for _, row := range result.Rows {
		if row["stage_id"] == stageID {
			return row
		}
	}
	t.Fatalf("no row for stage_id %q in %+v", stageID, result.Rows)
	return nil
}
