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
	"fmt"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/deals"
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

	result := e.runReport(e.Admin(), t, "deals-by-stage",
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

	result := e.runReport(e.Admin(), t, "deals-by-stage",
		`{"group_by":["stage_id"],"aggregates":[{"fn":"count","as":"deals"},{"fn":"sum","field":"amount_minor","as":"amount_minor_sum"},{"fn":"sum","field":"weighted_amount_minor","as":"weighted_minor"}]}`)
	row := dealsByStageRow(t, result, e.stages[60].String())
	handle, ok := row["derivation_url"].(string)
	if !ok || handle == "" {
		t.Fatalf("aggregate row has no derivation_url: %+v", row)
	}

	derivation := e.explainReport(e.Admin(), t, "deals-by-stage", handle)
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
	result := e.runReport(rep, t, "deals-by-stage",
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
	derivation := e.explainReport(rep, t, "deals-by-stage", handle)
	if derivation.TotalRows != 1 {
		t.Errorf("own-scope drill-through total = %d, want 1 (never the foreign deal)", derivation.TotalRows)
	}
}

// The board's per-column totals need deals-by-stage to accept
// every filter dial the board itself exposes, and to split a stage's total
// by currency so a mixed-currency column can still decline to sum (the same
// rule the board already gets right client-side, now proven server-side).

func TestDealsByStageGroupsByCurrencySeparately(t *testing.T) {
	e := setupForecast(t)
	e.seed(t, `INSERT INTO deal (id, workspace_id, name, pipeline_id, stage_id, amount_minor, currency, source, captured_by)
		VALUES ($1, $2, 'EUR deal', $3, $4, 100000, 'EUR', 'manual', 'human:x')`, e.pipeline, e.stages[60])
	e.seed(t, `INSERT INTO deal (id, workspace_id, name, pipeline_id, stage_id, amount_minor, currency, source, captured_by)
		VALUES ($1, $2, 'USD deal', $3, $4, 50000, 'USD', 'manual', 'human:x')`, e.pipeline, e.stages[60])

	result := e.runReport(e.Admin(), t, "deals-by-stage",
		`{"group_by":["stage_id","currency"],"aggregates":[{"fn":"count","as":"deals"},{"fn":"sum","field":"amount_minor","as":"amount_minor_sum"}]}`)
	var rows []map[string]any
	for _, row := range result.Rows {
		if row["stage_id"] == e.stages[60].String() {
			rows = append(rows, row)
		}
	}
	if len(rows) != 2 {
		t.Fatalf("rows for the stage = %d, want 2 (one per currency): %+v", len(rows), rows)
	}
	sums := map[string]int64{}
	for _, row := range rows {
		currency, ok := row["currency"].(string)
		if !ok {
			t.Fatalf("row %+v: currency cell is not a string", row)
		}
		sums[currency] = wireInt(t, row, "amount_minor_sum")
	}
	if sums["EUR"] != 100000 || sums["USD"] != 50000 {
		t.Errorf("currency-grouped sums = %+v, want EUR=100000 USD=50000", sums)
	}
}

func TestDealsByStageFiltersByOrganizationID(t *testing.T) {
	e := setupForecast(t)
	orgA := e.seed(t, `INSERT INTO organization (id, workspace_id, display_name, source, captured_by) VALUES ($1, $2, 'A', 'manual', 'human:x')`)
	orgB := e.seed(t, `INSERT INTO organization (id, workspace_id, display_name, source, captured_by) VALUES ($1, $2, 'B', 'manual', 'human:x')`)
	e.seed(t, `INSERT INTO deal (id, workspace_id, name, pipeline_id, stage_id, organization_id, amount_minor, currency, source, captured_by)
		VALUES ($1, $2, 'Deal A', $3, $4, $5, 10000, 'EUR', 'manual', 'human:x')`, e.pipeline, e.stages[60], orgA)
	e.seed(t, `INSERT INTO deal (id, workspace_id, name, pipeline_id, stage_id, organization_id, amount_minor, currency, source, captured_by)
		VALUES ($1, $2, 'Deal B', $3, $4, $5, 20000, 'EUR', 'manual', 'human:x')`, e.pipeline, e.stages[60], orgB)

	result := e.runReport(e.Admin(), t, "deals-by-stage", fmt.Sprintf(
		`{"group_by":["stage_id"],"aggregates":[{"fn":"count","as":"deals"},{"fn":"sum","field":"amount_minor","as":"amount_minor_sum"}],"filters":{"organization_id":%q}}`,
		orgA.String()))
	row := dealsByStageRow(t, result, e.stages[60].String())
	if got := wireInt(t, row, "deals"); got != 1 {
		t.Fatalf("deals = %d, want 1 (only org A's deal)", got)
	}
	if got := wireInt(t, row, "amount_minor_sum"); got != 10000 {
		t.Errorf("amount_minor_sum = %d, want 10000", got)
	}
}

func TestDealsByStageFiltersByPartnerSourced(t *testing.T) {
	e := setupForecast(t)
	partner := e.seed(t, `INSERT INTO organization (id, workspace_id, display_name, source, captured_by) VALUES ($1, $2, 'Partner', 'manual', 'human:x')`)
	e.seed(t, `INSERT INTO deal (id, workspace_id, name, pipeline_id, stage_id, partner_org_id, amount_minor, currency, source, captured_by)
		VALUES ($1, $2, 'Sourced', $3, $4, $5, 10000, 'EUR', 'manual', 'human:x')`, e.pipeline, e.stages[60], partner)
	e.seed(t, `INSERT INTO deal (id, workspace_id, name, pipeline_id, stage_id, amount_minor, currency, source, captured_by)
		VALUES ($1, $2, 'Direct', $3, $4, 20000, 'EUR', 'manual', 'human:x')`, e.pipeline, e.stages[60])

	result := e.runReport(e.Admin(), t, "deals-by-stage",
		`{"group_by":["stage_id"],"aggregates":[{"fn":"count","as":"deals"},{"fn":"sum","field":"amount_minor","as":"amount_minor_sum"}],"filters":{"partner_sourced":true}}`)
	row := dealsByStageRow(t, result, e.stages[60].String())
	if got := wireInt(t, row, "deals"); got != 1 {
		t.Fatalf("deals = %d, want 1 (only the partner-sourced deal)", got)
	}
	if got := wireInt(t, row, "amount_minor_sum"); got != 10000 {
		t.Errorf("amount_minor_sum = %d, want 10000", got)
	}
}

// deals-by-stage joins stage, which has its own created_at — so the stalled
// predicate (deals.StalledSQL) must reach this query alias-qualified, or an
// unqualified reference to a column both tables carry is ambiguous SQL. A
// regression to an unqualified spelling does not produce a wrong number; it
// 500s before ever reaching the assertions below.
func TestDealsByStageStalledFilterWorksUnderTheStageJoin(t *testing.T) {
	e := setupForecast(t)
	e.seed(t, `INSERT INTO deal (id, workspace_id, name, pipeline_id, stage_id, amount_minor, currency, source, captured_by, created_at)
		VALUES ($1, $2, 'Idle', $3, $4, 10000, 'EUR', 'manual', 'human:x', now() - interval '90 days')`, e.pipeline, e.stages[60])
	e.seedOpenDeal(t, "Fresh", 60, nil, int64p(20000), stringp("commit"))

	result := e.runReport(e.Admin(), t, "deals-by-stage",
		`{"group_by":["stage_id"],"aggregates":[{"fn":"count","as":"deals"},{"fn":"sum","field":"amount_minor","as":"amount_minor_sum"}],"filters":{"stalled":true}}`)
	row := dealsByStageRow(t, result, e.stages[60].String())
	if got := wireInt(t, row, "deals"); got != 1 {
		t.Fatalf("deals = %d, want 1 (only the idle deal)", got)
	}
	if got := wireInt(t, row, "amount_minor_sum"); got != 10000 {
		t.Errorf("amount_minor_sum = %d, want 10000", got)
	}
}

// The Go predicate (deals.IsStalled) and the SQL clause (deals.StalledSQL)
// are two spellings of the same rule (formulas-and-rules §8) and must agree.
// Margins here are wide (tens of days) on purpose: the SQL side evaluates
// against the live `now()` at query time, seconds after this test captured
// its own `now`, so a boundary-exact case would be flaky by construction —
// that exact case is formulas_test.go's job, against a fixed clock.
func TestDealsByStageStalledFilterAgreesWithIsStalled(t *testing.T) {
	e := setupForecast(t)
	now := time.Now().UTC()
	days := func(n int) time.Time { return now.AddDate(0, 0, n) }

	cases := []struct {
		name    string
		created time.Time
		lastAct *time.Time
		wait    *time.Time
	}{
		{"fresh", days(-5), timep(days(-2)), nil},
		{"idle past threshold", days(-90), timep(days(-70)), nil},
		{"active wait suppresses", days(-90), timep(days(-80)), timep(days(10))},
		{"expired wait un-suppresses", days(-90), timep(days(-80)), timep(days(-5))},
	}
	for _, c := range cases {
		e.seed(t, `INSERT INTO deal (id, workspace_id, name, pipeline_id, stage_id, amount_minor, currency, created_at, last_activity_at, wait_until, source, captured_by)
			VALUES ($1, $2, $3, $4, $5, 1000, 'EUR', $6, $7, $8, 'manual', 'human:x')`,
			c.name, e.pipeline, e.stages[60], c.created, c.lastAct, c.wait)
	}

	result := e.runReport(e.Admin(), t, "deals-by-stage",
		`{"group_by":["stage_id"],"aggregates":[{"fn":"count","as":"deals"}],"filters":{"stalled":true}}`)
	row := dealsByStageRow(t, result, e.stages[60].String())

	var wantStalled int64
	for _, c := range cases {
		if deals.IsStalled("open", c.created, c.lastAct, c.wait, now) {
			wantStalled++
		}
	}
	if wantStalled == 0 || wantStalled == int64(len(cases)) {
		t.Fatalf("fixture is broken: IsStalled must split these %d cases, not agree on all of them (got %d stalled)", len(cases), wantStalled)
	}
	if got := wireInt(t, row, "deals"); got != wantStalled {
		t.Errorf("SQL filter matched %d deals, Go's IsStalled agrees on %d — the two spellings of §8 have drifted", got, wantStalled)
	}
}

func timep(v time.Time) *time.Time { return &v }

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
