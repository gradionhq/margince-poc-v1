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
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
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

// Deals are readable by every seat that holds the deal grant, whatever the
// seat's own/team/all tier: a own-scoped rep's deals-by-stage counts another
// rep's deal in the same stage, and the drill-through returns the SAME set the
// aggregate counted — the same read model the forecast suite proves in
// TestForecastDerivationReadsEveryDealWhateverTheTier.
func TestDealsByStageCountsEveryDealWhateverTheTier(t *testing.T) {
	e := setupForecast(t)
	e.seedOpenDeal(t, "Mine", 60, &e.Rep1, int64p(10000), stringp("commit"))
	e.seedOpenDeal(t, "Theirs", 60, &e.Rep3, int64p(20000), stringp("commit"))

	rep := e.dealReadCtx(e.Rep1, nil, principal.RowScopeOwn)
	result := e.runReport(rep, t, "deals-by-stage",
		`{"group_by":["stage_id"],"aggregates":[{"fn":"count","as":"deals"},{"fn":"sum","field":"amount_minor","as":"amount_minor_sum"},{"fn":"sum","field":"weighted_amount_minor","as":"weighted_minor"}]}`)
	row := dealsByStageRow(t, result, e.stages[60].String())
	if got := wireInt(t, row, "deals"); got != 2 {
		t.Fatalf("deals = %d, want 2 — a deal is readable by every seat, the other rep's included", got)
	}
	if want := weightedMinor(10000, 60) + weightedMinor(20000, 60); wireInt(t, row, "weighted_minor") != want {
		t.Errorf("weighted_minor = %d, want %d (both deals in the stage)", wireInt(t, row, "weighted_minor"), want)
	}

	handle, ok := row["derivation_url"].(string)
	if !ok || handle == "" {
		t.Fatalf("aggregate row has no derivation_url: %+v", row)
	}
	derivation := e.explainReport(rep, t, "deals-by-stage", handle)
	if derivation.TotalRows != 2 {
		t.Errorf("own-scope drill-through total = %d, want the 2 deals the aggregate counted", derivation.TotalRows)
	}
}

// The board's per-column totals need deals-by-stage to accept
// every filter dial the board itself exposes, and to split a stage's total
// by currency so a mixed-currency column can still decline to sum (the same
// rule the board already gets right client-side, now proven server-side).

func TestDealsByStageGroupsByCurrencySeparately(t *testing.T) {
	e := setupForecast(t)
	e.seedID(t, `INSERT INTO deal (id, name, pipeline_id, stage_id, amount_minor, currency, source, captured_by)
		VALUES ($1, 'EUR deal', $2, $3, 100000, 'EUR', 'manual', 'human:x')`, e.pipeline, e.stages[60])
	e.seedID(t, `INSERT INTO deal (id, name, pipeline_id, stage_id, amount_minor, currency, source, captured_by)
		VALUES ($1, 'USD deal', $2, $3, 50000, 'USD', 'manual', 'human:x')`, e.pipeline, e.stages[60])

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
	orgA := e.seedID(t, `INSERT INTO organization (id, display_name, source, captured_by) VALUES ($1, 'A', 'manual', 'human:x')`)
	orgB := e.seedID(t, `INSERT INTO organization (id, display_name, source, captured_by) VALUES ($1, 'B', 'manual', 'human:x')`)
	e.seedID(t, `INSERT INTO deal (id, name, pipeline_id, stage_id, organization_id, amount_minor, currency, source, captured_by)
		VALUES ($1, 'Deal A', $2, $3, $4, 10000, 'EUR', 'manual', 'human:x')`, e.pipeline, e.stages[60], orgA)
	e.seedID(t, `INSERT INTO deal (id, name, pipeline_id, stage_id, organization_id, amount_minor, currency, source, captured_by)
		VALUES ($1, 'Deal B', $2, $3, $4, 20000, 'EUR', 'manual', 'human:x')`, e.pipeline, e.stages[60], orgB)

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

// The question the partner program is actually run on: what has each partner
// brought us. It was unanswerable while partner_sourced was a filter alone —
// the report could say "these deals came from some partner" and never say which.
func TestDealsByStageGroupsRevenueByPartner(t *testing.T) {
	e := setupForecast(t)
	northgate := e.seedID(t, `INSERT INTO organization (id, display_name, source, captured_by) VALUES ($1, 'Northgate', 'manual', 'human:x')`)
	kestrel := e.seedID(t, `INSERT INTO organization (id, display_name, source, captured_by) VALUES ($1, 'Kestrel', 'manual', 'human:x')`)
	e.seedID(t, `INSERT INTO deal (id, name, pipeline_id, stage_id, partner_org_id, partner_attribution, amount_minor, currency, source, captured_by)
		VALUES ($1, 'Northgate one', $2, $3, $4, 'sourced', 30000, 'EUR', 'manual', 'human:x')`, e.pipeline, e.stages[60], northgate)
	e.seedID(t, `INSERT INTO deal (id, name, pipeline_id, stage_id, partner_org_id, partner_attribution, amount_minor, currency, source, captured_by)
		VALUES ($1, 'Northgate two', $2, $3, $4, 'influenced', 20000, 'EUR', 'manual', 'human:x')`, e.pipeline, e.stages[60], northgate)
	e.seedID(t, `INSERT INTO deal (id, name, pipeline_id, stage_id, partner_org_id, partner_attribution, amount_minor, currency, source, captured_by)
		VALUES ($1, 'Kestrel one', $2, $3, $4, 'sourced', 70000, 'EUR', 'manual', 'human:x')`, e.pipeline, e.stages[60], kestrel)

	result := e.runReport(e.Admin(), t, "deals-by-stage",
		`{"group_by":["partner_org_id"],"aggregates":[{"fn":"count","as":"deals"},{"fn":"sum","field":"amount_minor","as":"amount_minor_sum"}],"filters":{"partner_sourced":true}}`)

	byPartner := map[string]int64{}
	for _, row := range result.Rows {
		id, ok := row["partner_org_id"].(string)
		if !ok {
			continue
		}
		byPartner[id] = wireInt(t, row, "amount_minor_sum")
	}
	if got := byPartner[northgate.String()]; got != 50000 {
		t.Errorf("Northgate total = %d, want 50000 (both of their deals)", got)
	}
	if got := byPartner[kestrel.String()]; got != 70000 {
		t.Errorf("Kestrel total = %d, want 70000", got)
	}
}

// Grouping by partner must not report a partner the caller could not open.
//
// A normal deal read masks partner_org_id per row when the referenced
// organization is out of reach (deals/fieldmask.go). The report engine gates
// only the deal entity, so without the reference-scope clause an aggregate
// would hand back exactly the id the same caller's own read withholds — and an
// aggregate has no per-row place to write "withheld".
func TestGroupingByPartnerDoesNotNameAPartnerTheCallerCannotOpen(t *testing.T) {
	e := setupForecast(t)
	// Capture-private to Rep3: readable to its owner, invisible to every other
	// seat, exactly as a connector-captured company can be.
	hidden := e.seedID(t, `INSERT INTO organization (id, owner_id, display_name, visibility, source, captured_by)
		VALUES ($1, $2, 'Hidden Partners', 'owner', 'manual', 'human:x')`, e.Rep3)
	open := e.seedID(t, `INSERT INTO organization (id, display_name, source, captured_by)
		VALUES ($1, 'Open Partners', 'manual', 'human:x')`)
	e.seedID(t, `INSERT INTO deal (id, name, pipeline_id, stage_id, partner_org_id, partner_attribution, amount_minor, currency, source, captured_by)
		VALUES ($1, 'From the hidden partner', $2, $3, $4, 'sourced', 90000, 'EUR', 'manual', 'human:x')`, e.pipeline, e.stages[60], hidden)
	e.seedID(t, `INSERT INTO deal (id, name, pipeline_id, stage_id, partner_org_id, partner_attribution, amount_minor, currency, source, captured_by)
		VALUES ($1, 'From the open partner', $2, $3, $4, 'sourced', 10000, 'EUR', 'manual', 'human:x')`, e.pipeline, e.stages[60], open)

	// A reader of every deal who holds no organization grant at all.
	reader := e.dealReadCtx(ids.NewV7(), nil, principal.RowScopeAll)
	result := e.runReport(reader, t, "deals-by-stage",
		`{"group_by":["partner_org_id"],"aggregates":[{"fn":"count","as":"deals"},{"fn":"sum","field":"amount_minor","as":"amount_minor_sum"}],"filters":{"partner_sourced":true}}`)

	for _, row := range result.Rows {
		if id, ok := row["partner_org_id"].(string); ok && id == hidden.String() {
			t.Errorf("the report named partner %s, which this caller's own deal read masks", id)
		}
	}
	// The partner they CAN open is still reported: the clause narrows, it does
	// not blank the whole dimension.
	var sawOpen bool
	for _, row := range result.Rows {
		if id, ok := row["partner_org_id"].(string); ok && id == open.String() {
			sawOpen = true
			if got := wireInt(t, row, "amount_minor_sum"); got != 10000 {
				t.Errorf("open partner total = %d, want 10000", got)
			}
		}
	}
	if !sawOpen {
		t.Error("the readable partner vanished too; the clause excluded more than it should")
	}
}

func TestDealsByStageFiltersByPartnerSourced(t *testing.T) {
	e := setupForecast(t)
	partner := e.seedID(t, `INSERT INTO organization (id, display_name, source, captured_by) VALUES ($1, 'Partner', 'manual', 'human:x')`)
	e.seedID(t, `INSERT INTO deal (id, name, pipeline_id, stage_id, partner_org_id, partner_attribution, amount_minor, currency, source, captured_by)
		VALUES ($1, 'Sourced', $2, $3, $4, 'sourced', 10000, 'EUR', 'manual', 'human:x')`, e.pipeline, e.stages[60], partner)
	e.seedID(t, `INSERT INTO deal (id, name, pipeline_id, stage_id, amount_minor, currency, source, captured_by)
		VALUES ($1, 'Direct', $2, $3, 20000, 'EUR', 'manual', 'human:x')`, e.pipeline, e.stages[60])

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
	e.seedID(t, `INSERT INTO deal (id, name, pipeline_id, stage_id, amount_minor, currency, source, captured_by, created_at)
		VALUES ($1, 'Idle', $2, $3, 10000, 'EUR', 'manual', 'human:x', now() - interval '90 days')`, e.pipeline, e.stages[60])
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
		e.seedID(t, `INSERT INTO deal (id, name, pipeline_id, stage_id, amount_minor, currency, created_at, last_activity_at, wait_until, source, captured_by)
			VALUES ($1, $2, $3, $4, 1000, 'EUR', $5, $6, $7, 'manual', 'human:x')`,
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
