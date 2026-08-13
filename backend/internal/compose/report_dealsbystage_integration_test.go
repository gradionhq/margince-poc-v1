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
)

func (e *forecastEnv) runDealsByStage(t *testing.T, ctx context.Context, body string) reportResultWire {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/reports/deals-by-stage", strings.NewReader(body)).WithContext(ctx)
	rec := httptest.NewRecorder()
	e.handlers.RunReport(rec, req, "deals-by-stage")
	var result reportResultWire
	decodeWire(t, rec, http.StatusOK, &result)
	return result
}

func TestDealsByStageWeightedReconcilesToPerDealRounding(t *testing.T) {
	e := setupForecast(t)

	// Amounts chosen so per-deal rounding is exercised (12341×60% is not
	// whole after /100): round(Σamount×p/100) and Σround(amount×p/100)
	// disagree for this exact stage's constituents.
	e.seedOpenDeal(t, "Alpha", 60, nil, int64p(100000), stringp("commit"))
	e.seedOpenDeal(t, "Beta", 60, nil, int64p(12341), stringp("commit"))

	result := e.runDealsByStage(t, e.Admin(), fmt.Sprintf(
		`{"group_by":["stage_id"],"aggregates":[{"fn":"count","as":"deals"},{"fn":"sum","field":"amount_minor","as":"amount_minor_sum"},{"fn":"sum","field":"weighted_amount_minor","as":"weighted_minor"}],"filters":{"stage_id":%q}}`,
		e.stages[60].String()))
	if len(result.Rows) != 1 {
		t.Fatalf("rows = %+v, want exactly one stage group", result.Rows)
	}
	row := result.Rows[0]

	wantWeighted := weightedMinor(100000, 60) + weightedMinor(12341, 60)
	wrongWeighted := int64(100000+12341) * 60 / 100 // round(Σamount × p / 100): the bug this measure fixes
	if got := wireInt(t, row, "weighted_minor"); got != wantWeighted {
		if got == wrongWeighted {
			t.Fatalf("weighted_minor = %d (round(sum)×p/100), want %d (Σround(amount×p/100) — per-deal rounding)", got, wantWeighted)
		}
		t.Fatalf("weighted_minor = %d, want %d", got, wantWeighted)
	}
	if got := wireInt(t, row, "amount_minor_sum"); got != 100000+12341 {
		t.Errorf("amount_minor_sum = %d, want %d", got, 100000+12341)
	}
}

// The measure is published on the catalog and readable through
// "Explain This Number" like every other measure on this report — no
// second, undocumented vocabulary.
func TestDealsByStageWeightedIsInTheCatalog(t *testing.T) {
	spec, ok := prebuiltReports["deals-by-stage"]
	if !ok {
		t.Fatal("deals-by-stage is not a served report")
	}
	if _, ok := spec.measures["weighted_amount_minor"]; !ok {
		t.Fatal("deals-by-stage has no weighted_amount_minor measure")
	}
	if _, ok := spec.dimensions["win_probability"]; !ok {
		t.Fatal("deals-by-stage has no win_probability dimension to derive the weighted measure from")
	}
	found := false
	for _, entry := range reportToolCatalog() {
		if entry.Report != "deals-by-stage" {
			continue
		}
		for _, name := range entry.Aggregates {
			if name == "weighted_amount_minor" {
				found = true
			}
		}
	}
	if !found {
		t.Error("reportToolCatalog does not advertise deals-by-stage's weighted_amount_minor measure")
	}
}
