// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The deals-by-stage report's weighted figure (AC-F1): the
// per-stage weighted total must equal the sum of each constituent deal's
// OWN rounded weighted value, not the stage's probability applied once to
// the summed raw total — the same invariant the forecast report already
// proves for itself in report_forecast_integration_test.go. The board
// (frontend/src/screens/deals.tsx) and the reports screen's deals-by-stage
// table both need this figure; neither carries per-deal rows to round
// client-side, so the engine has to compute it the AC-F1 way.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
)

func (e *forecastEnv) runDealsByStage(ctx context.Context, t *testing.T, body string) reportResultWire {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/reports/deals-by-stage", strings.NewReader(body)).WithContext(ctx)
	rec := httptest.NewRecorder()
	e.handlers.RunReport(rec, req, "deals-by-stage")
	var result reportResultWire
	decodeWire(t, rec, http.StatusOK, &result)
	return result
}

func (e *forecastEnv) explainDealsByStage(ctx context.Context, t *testing.T, handleURL string) derivationWire {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, handleURL, nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	e.handlers.ExplainReport(rec, req, "deals-by-stage", crmcontracts.ExplainReportParams{})
	var result derivationWire
	decodeWire(t, rec, http.StatusOK, &result)
	return result
}

func TestDealsByStageWeightedReconcilesToPerDealRounding(t *testing.T) {
	e := setupForecast(t)

	// Two equal deals whose OWN weighted values each round UP (12341×60% =
	// 7404.6 → 7405), while their combined raw total rounds DOWN
	// (24682×60% = 14809.2 → 14809): round(Σamount×p/100) and
	// Σround(amount×p/100) disagree by exactly 1 for this stage.
	const dealAmount = int64(12341)
	e.seedOpenDeal(t, "Alpha", 60, nil, int64p(dealAmount), stringp("commit"))
	e.seedOpenDeal(t, "Beta", 60, nil, int64p(dealAmount), stringp("commit"))

	result := e.runDealsByStage(e.Admin(), t, fmt.Sprintf(
		`{"group_by":["stage_id"],"aggregates":[{"fn":"count","as":"deals"},{"fn":"sum","field":"amount_minor","as":"amount_minor_sum"},{"fn":"sum","field":"weighted_amount_minor","as":"weighted_minor"}],"filters":{"stage_id":%q}}`,
		e.stages[60].String()))
	if len(result.Rows) != 1 {
		t.Fatalf("rows = %+v, want exactly one stage group", result.Rows)
	}
	row := result.Rows[0]

	wantWeighted := weightedMinor(dealAmount, 60) + weightedMinor(dealAmount, 60)
	wrongWeighted := weightedMinor(2*dealAmount, 60) // round(Σamount × p / 100): the bug this measure fixes
	if wantWeighted == wrongWeighted {
		t.Fatal("fixture is broken: the two methodologies agree, so this test cannot discriminate between them")
	}
	if got := wireInt(t, row, "weighted_minor"); got != wantWeighted {
		if got == wrongWeighted {
			t.Fatalf("weighted_minor = %d (round(sum)×p/100), want %d (Σround(amount×p/100) — per-deal rounding)", got, wantWeighted)
		}
		t.Fatalf("weighted_minor = %d, want %d", got, wantWeighted)
	}
	if got := wireInt(t, row, "amount_minor_sum"); got != 2*dealAmount {
		t.Errorf("amount_minor_sum = %d, want %d", got, 2*dealAmount)
	}
}

// The measure's vocabulary membership (catalog advertises it, engine
// accepts it) is already proven generically, for every measure of every
// report, by TestReportToolCatalogPublishesOnlyWhatTheEngineAccepts and
// TestReportToolCatalogOmitsNothingTheEngineAccepts in reportcatalog_test.go
// — both derive their expectations from prebuiltReports, so this measure
// is covered the moment it exists in the spec. What those generic tests
// cannot prove is that "Explain This Number" resolves it correctly: the
// drill-through source rows must carry weighted_amount_minor and reconcile
// exactly to the displayed weighted_minor, the same AC-F1 guarantee the
// forecast report proves for itself.
func TestDealsByStageWeightedDerivationReconcilesExactly(t *testing.T) {
	e := setupForecast(t)
	e.seedOpenDeal(t, "Alpha", 60, nil, int64p(12341), stringp("commit"))
	e.seedOpenDeal(t, "Beta", 60, nil, int64p(12341), stringp("commit"))
	// A different stage's deal must not leak into the group being explained.
	e.seedOpenDeal(t, "Elsewhere", 20, nil, int64p(999999), stringp("commit"))

	result := e.runDealsByStage(e.Admin(), t, fmt.Sprintf(
		`{"group_by":["stage_id"],"aggregates":[{"fn":"count","as":"deals"},{"fn":"sum","field":"amount_minor","as":"amount_minor_sum"},{"fn":"sum","field":"weighted_amount_minor","as":"weighted_minor"}],"filters":{"stage_id":%q}}`,
		e.stages[60].String()))
	if len(result.Rows) != 1 {
		t.Fatalf("rows = %+v, want exactly one stage group", result.Rows)
	}
	row := result.Rows[0]
	handle, ok := row["derivation_url"].(string)
	if !ok || handle == "" {
		t.Fatalf("aggregate row has no derivation_url: %+v", row)
	}

	derivation := e.explainDealsByStage(e.Admin(), t, handle)
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
