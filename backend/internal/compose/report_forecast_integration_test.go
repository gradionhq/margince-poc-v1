// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package compose

// The forecast roll-up (B-E09.10) and the "Explain This Number"
// derivation (B-E09.9) over the real migrated Postgres: weighted +
// unweighted totals reconcile exactly to the seeded constituent deals
// (AC-F1), a multi-stakeholder deal counts once (AC-F2), a drill-through
// sums exactly to the aggregate it explains (AC-R6/AC-X1), and the
// explanation never out-sees the report's row scope.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	crmcontracts "github.com/gradionhq/margince/backend/internal/contracts"
	"github.com/gradionhq/margince/backend/internal/platform/database"
	"github.com/gradionhq/margince/backend/internal/platform/dbmigrate"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
	"github.com/gradionhq/margince/backend/migrations"
)

type forecastEnv struct {
	owner    *pgx.Conn
	pool     *pgxpool.Pool
	handlers reportHandlers
	ws       ids.UUID
	rep1     ids.UUID // team1
	rep3     ids.UUID // team2
	team1    ids.UUID
	team2    ids.UUID
	pipeline ids.UUID
	// stages keyed by win_probability, all semantic=open
	stages map[int]ids.UUID
}

func setupForecast(t *testing.T) *forecastEnv {
	t.Helper()
	ownerDSN := os.Getenv("MARGINCE_TEST_DSN")
	appDSN := os.Getenv("MARGINCE_TEST_APP_DSN")
	if ownerDSN == "" || appDSN == "" {
		t.Fatal("MARGINCE_TEST_DSN / MARGINCE_TEST_APP_DSN not set — run `make db-up` (integration tests fail loudly, they never skip)")
	}
	ctx := context.Background()
	owner, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := owner.Close(context.Background()); err != nil {
			t.Errorf("closing owner connection: %v", err)
		}
	})
	if _, err := owner.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public; GRANT USAGE ON SCHEMA public TO margince_app`); err != nil {
		t.Fatal(err)
	}
	core, err := migrations.Core()
	if err != nil {
		t.Fatal(err)
	}
	custom, err := migrations.Custom()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dbmigrate.Up(ctx, owner, core, custom); err != nil {
		t.Fatal(err)
	}

	e := &forecastEnv{
		owner: owner, ws: ids.NewV7(),
		rep1: ids.NewV7(), rep3: ids.NewV7(), team1: ids.NewV7(), team2: ids.NewV7(),
		stages: map[int]ids.UUID{},
	}
	if _, err := owner.Exec(ctx, `INSERT INTO workspace (id, name, slug, base_currency) VALUES ($1, 'Forecast', 'forecast', 'EUR')`, e.ws); err != nil {
		t.Fatal(err)
	}
	for email, u := range map[string]ids.UUID{"rep1@forecast.test": e.rep1, "rep3@forecast.test": e.rep3} {
		if _, err := owner.Exec(ctx, `INSERT INTO app_user (id, workspace_id, email, display_name) VALUES ($1, $2, $3, 'Rep')`, u, e.ws, email); err != nil {
			t.Fatal(err)
		}
	}
	for _, tm := range []ids.UUID{e.team1, e.team2} {
		if _, err := owner.Exec(ctx, `INSERT INTO team (id, workspace_id, name) VALUES ($1, $2, $3)`, tm, e.ws, tm.String()); err != nil {
			t.Fatal(err)
		}
	}
	for u, tm := range map[ids.UUID]ids.UUID{e.rep1: e.team1, e.rep3: e.team2} {
		if _, err := owner.Exec(ctx, `INSERT INTO team_membership (workspace_id, team_id, user_id) VALUES ($1, $2, $3)`, e.ws, tm, u); err != nil {
			t.Fatal(err)
		}
	}

	e.pipeline = e.seed(t, `INSERT INTO pipeline (id, workspace_id, name, is_default, position) VALUES ($1, $2, 'Sales', true, 0)`)
	for position, probability := range map[int]int{0: 20, 1: 55, 2: 60} {
		e.stages[probability] = e.seed(t,
			`INSERT INTO stage (id, workspace_id, pipeline_id, name, position, semantic, win_probability) VALUES ($1, $2, $3, $4, $5, 'open', $6)`,
			e.pipeline, fmt.Sprintf("Stage %d", position), position, probability)
	}

	pool, err := database.NewPool(ctx, appDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	e.pool = pool
	e.handlers = reportHandlers{engine: newReportEngine(pool)}
	return e
}

// seed writes rows through the owner connection: these suites test READ
// semantics; the write shape has its own suites.
func (e *forecastEnv) seed(t *testing.T, sql string, args ...any) ids.UUID {
	t.Helper()
	id := ids.NewV7()
	if _, err := e.owner.Exec(context.Background(), sql, append([]any{id, e.ws}, args...)...); err != nil {
		t.Fatalf("seeding: %v", err)
	}
	return id
}

// seedOpenDeal plants one live open deal; amountMinor/category/owner may
// be nil (the honest NULL cases every roll-up must survive). The close
// date is comfortably future so the §11 hygiene exclusion stays out of
// these roll-up suites — the exclusion has its own suite.
func (e *forecastEnv) seedOpenDeal(t *testing.T, name string, probability int, owner *ids.UUID, amountMinor *int64, category *string) ids.UUID {
	t.Helper()
	return e.seed(t, `INSERT INTO deal (id, workspace_id, name, pipeline_id, stage_id, owner_id, amount_minor, currency, forecast_category, expected_close_date, source, captured_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'EUR', $8, (now() + interval '30 days')::date, 'manual', 'human:x')`,
		name, e.pipeline, e.stages[probability], owner, amountMinor, category)
}

func (e *forecastEnv) dealReadCtx(userID ids.UUID, teams []ids.UUID, scope principal.RowScope) context.Context {
	ctx := principal.WithWorkspaceID(context.Background(), e.ws)
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalHuman, ID: "human:" + userID.String(), UserID: userID, TeamIDs: teams,
		Permissions: principal.Permissions{
			Objects:  map[string]principal.ObjectGrant{"deal": {Read: true}},
			RowScope: scope,
		},
	})
}

func (e *forecastEnv) admin() context.Context {
	return e.dealReadCtx(ids.NewV7(), nil, principal.RowScopeAll)
}

type reportResultWire struct {
	Report        string           `json:"report"`
	Columns       []string         `json:"columns"`
	Rows          []map[string]any `json:"rows"`
	TotalRows     int              `json:"total_rows"`
	DerivationURL string           `json:"derivation_url"`
}

type derivationWire struct {
	Report     string           `json:"report"`
	Definition string           `json:"definition"`
	Columns    []string         `json:"columns"`
	Rows       []map[string]any `json:"rows"`
	Aggregates map[string]any   `json:"aggregates"`
	TotalRows  int              `json:"total_rows"`
}

//craft:ignore naked-any decodeWire is the one JSON unmarshal seam; the wire structs above give it shape
func decodeWire(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int, into any) {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, wantStatus, rec.Body.String())
	}
	dec := json.NewDecoder(rec.Body)
	dec.UseNumber()
	if err := dec.Decode(into); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
}

func (e *forecastEnv) runForecast(t *testing.T, ctx context.Context, body string) reportResultWire {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/reports/forecast", strings.NewReader(body)).WithContext(ctx)
	rec := httptest.NewRecorder()
	e.handlers.RunReport(rec, req, "forecast")
	var result reportResultWire
	decodeWire(t, rec, http.StatusOK, &result)
	return result
}

func (e *forecastEnv) explain(t *testing.T, ctx context.Context, handleURL string) derivationWire {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, handleURL, nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	e.handlers.ExplainReport(rec, req, "forecast", crmcontracts.ExplainReportParams{})
	var result derivationWire
	decodeWire(t, rec, http.StatusOK, &result)
	return result
}

// wireInt reads a JSON-decoded numeric cell exactly (UseNumber keeps
// bigint sums out of float64).
func wireInt(t *testing.T, row map[string]any, key string) int64 {
	t.Helper()
	num, ok := row[key].(json.Number)
	if !ok {
		t.Fatalf("cell %q = %v (%T), want a number", key, row[key], row[key])
	}
	v, err := num.Int64()
	if err != nil {
		t.Fatalf("cell %q = %v: %v", key, num, err)
	}
	return v
}

// weightedMinor mirrors formulas-and-rules §6: round(amount ×
// probability / 100) per deal, half away from zero — the ground truth
// the report must reconcile to.
func weightedMinor(amountMinor int64, probability int64) int64 {
	return (amountMinor*probability + 50) / 100
}

func int64p(v int64) *int64    { return &v }
func stringp(v string) *string { return &v }

func TestForecastRollupReconcilesToConstituentDeals(t *testing.T) {
	e := setupForecast(t)

	// The constituent open deals — amounts chosen so per-deal rounding
	// is exercised (12341×60% and 54321×55% are not whole after /100).
	type constituent struct {
		amount      *int64
		probability int64
		category    *string
	}
	constituents := []constituent{
		{int64p(100000), 20, stringp("commit")},
		{int64p(12341), 60, stringp("commit")},
		{nil, 60, stringp("commit")}, // no amount: counted, sums untouched
		{int64p(999), 55, stringp("best_case")},
		{int64p(54321), 55, nil}, // no category: the NULL group
	}
	for i, c := range constituents {
		e.seedOpenDeal(t, fmt.Sprintf("Deal %d", i), int(c.probability), nil, c.amount, c.category)
	}
	// Closed and archived deals are not forecast.
	e.seed(t, `INSERT INTO deal (id, workspace_id, name, pipeline_id, stage_id, status, closed_at, source, captured_by)
		VALUES ($1, $2, 'Won already', $3, $4, 'won', now(), 'manual', 'human:x')`, e.pipeline, e.stages[60])
	e.seed(t, `INSERT INTO deal (id, workspace_id, name, pipeline_id, stage_id, amount_minor, currency, archived_at, source, captured_by)
		VALUES ($1, $2, 'Archived', $3, $4, 77777, 'EUR', now(), 'manual', 'human:x')`, e.pipeline, e.stages[60])

	result := e.runForecast(t, e.admin(), `{"group_by":["forecast_category"]}`)
	if len(result.Rows) != 3 {
		t.Fatalf("rows = %d (%+v), want commit + best_case + the NULL group", len(result.Rows), result.Rows)
	}
	if result.DerivationURL == "" {
		t.Error("result-level derivation_url missing")
	}

	// AC-F1: per-group AND overall, weighted + unweighted totals equal
	// the sum over the seeded constituent deals — zero tolerance.
	wantByCategory := map[string]struct{ deals, unweighted, weighted int64 }{}
	for _, c := range constituents {
		key := ""
		if c.category != nil {
			key = *c.category
		}
		want := wantByCategory[key]
		want.deals++
		if c.amount != nil {
			want.unweighted += *c.amount
			want.weighted += weightedMinor(*c.amount, c.probability)
		}
		wantByCategory[key] = want
	}
	var gotDeals, gotUnweighted, gotWeighted int64
	for _, row := range result.Rows {
		key := ""
		if s, ok := row["forecast_category"].(string); ok {
			key = s
		}
		want, ok := wantByCategory[key]
		if !ok {
			t.Fatalf("unexpected group %q: %+v", key, row)
		}
		if url, ok := row["derivation_url"].(string); !ok || url == "" {
			t.Errorf("group %q: aggregate row without a derivation_url handle", key)
		}
		if got := wireInt(t, row, "deals"); got != want.deals {
			t.Errorf("group %q deals = %d, want %d", key, got, want.deals)
		}
		if got := wireInt(t, row, "unweighted_minor"); got != want.unweighted {
			t.Errorf("group %q unweighted = %d, want %d", key, got, want.unweighted)
		}
		if got := wireInt(t, row, "weighted_minor"); got != want.weighted {
			t.Errorf("group %q weighted = %d, want %d", key, got, want.weighted)
		}
		gotDeals += wireInt(t, row, "deals")
		gotUnweighted += wireInt(t, row, "unweighted_minor")
		gotWeighted += wireInt(t, row, "weighted_minor")
	}
	if gotDeals != 5 || gotUnweighted != 100000+12341+999+54321 ||
		gotWeighted != weightedMinor(100000, 20)+weightedMinor(12341, 60)+weightedMinor(999, 55)+weightedMinor(54321, 55) {
		t.Errorf("roll-up total = (%d, %d, %d) deals/unweighted/weighted — does not reconcile to the constituent deals",
			gotDeals, gotUnweighted, gotWeighted)
	}
}

// AC-F2: the roll-up aggregates deals, never deal×stakeholder join rows —
// a deal with two stakeholders counts once in the per-owner grouping.
func TestForecastByOwnerCountsAMultiStakeholderDealOnce(t *testing.T) {
	e := setupForecast(t)
	dealID := e.seedOpenDeal(t, "Two champions", 60, &e.rep1, int64p(50000), stringp("commit"))
	for _, role := range []string{"champion", "economic_buyer"} {
		personID := e.seed(t, `INSERT INTO person (id, workspace_id, full_name, source, captured_by) VALUES ($1, $2, $3, 'manual', 'human:x')`, "Stakeholder "+role)
		e.seed(t, `INSERT INTO relationship (id, workspace_id, kind, deal_id, person_id, role, source, captured_by)
			VALUES ($1, $2, 'deal_stakeholder', $3, $4, $5, 'manual', 'human:x')`, dealID, personID, role)
	}

	result := e.runForecast(t, e.admin(), `{"group_by":["owner_id"]}`)
	if len(result.Rows) != 1 {
		t.Fatalf("rows = %+v, want exactly one owner group", result.Rows)
	}
	row := result.Rows[0]
	if row["owner_id"] != e.rep1.String() {
		t.Fatalf("owner_id = %v, want %s", row["owner_id"], e.rep1)
	}
	if got := wireInt(t, row, "deals"); got != 1 {
		t.Errorf("deals = %d, want 1 — the stakeholder join must not multiply the deal", got)
	}
	if got := wireInt(t, row, "unweighted_minor"); got != 50000 {
		t.Errorf("unweighted = %d, want 50000", got)
	}
	if got := wireInt(t, row, "weighted_minor"); got != 30000 {
		t.Errorf("weighted = %d, want 30000", got)
	}
}

// AC-R6 + AC-X1: resolving an aggregate row's derivation_url returns a
// plain-language definition and source rows that sum EXACTLY to the
// displayed aggregate; each source row carries the weighted value next
// to its base inputs, so the lineage bottoms out with no opaque step.
func TestForecastDerivationDrillThroughReconcilesExactly(t *testing.T) {
	e := setupForecast(t)
	e.seedOpenDeal(t, "Alpha", 20, &e.rep1, int64p(100000), stringp("commit"))
	e.seedOpenDeal(t, "Beta", 60, &e.rep1, int64p(12341), stringp("best_case"))
	e.seedOpenDeal(t, "Gamma", 55, &e.rep1, nil, stringp("commit"))
	e.seedOpenDeal(t, "Foreign owner", 60, &e.rep3, int64p(999999), stringp("commit"))

	result := e.runForecast(t, e.admin(), `{"group_by":["owner_id"]}`)
	var row map[string]any
	for _, r := range result.Rows {
		if r["owner_id"] == e.rep1.String() {
			row = r
		}
	}
	if row == nil {
		t.Fatalf("no aggregate row for rep1: %+v", result.Rows)
	}

	handle, ok := row["derivation_url"].(string)
	if !ok || handle == "" {
		t.Fatalf("aggregate row has no derivation_url: %+v", row)
	}
	derivation := e.explain(t, e.admin(), handle)

	for _, phrase := range []string{
		"open, unarchived deals",
		`within the group where owner_id = "` + e.rep1.String() + `"`,
		"the sum of weighted_amount_minor as weighted_minor",
	} {
		if !strings.Contains(derivation.Definition, phrase) {
			t.Errorf("definition %q lacks %q", derivation.Definition, phrase)
		}
	}

	if len(derivation.Rows) != 3 || derivation.TotalRows != 3 {
		t.Fatalf("drill-through = %d rows (total %d), want rep1's 3 deals: %+v",
			len(derivation.Rows), derivation.TotalRows, derivation.Rows)
	}
	var unweighted, weighted int64
	for _, source := range derivation.Rows {
		if source["amount_minor"] == nil {
			if source["weighted_amount_minor"] != nil {
				t.Errorf("a NULL-amount deal grew a weighted value: %+v", source)
			}
			continue
		}
		amount := wireInt(t, source, "amount_minor")
		probability := wireInt(t, source, "win_probability")
		rowWeighted := wireInt(t, source, "weighted_amount_minor")
		if rowWeighted != weightedMinor(amount, probability) {
			t.Errorf("source row weighted = %d, want round(%d × %d%%) = %d — the derived input must expose its own lineage",
				rowWeighted, amount, probability, weightedMinor(amount, probability))
		}
		unweighted += amount
		weighted += rowWeighted
	}
	if unweighted != wireInt(t, row, "unweighted_minor") {
		t.Errorf("drill-through unweighted sum %d != displayed %d", unweighted, wireInt(t, row, "unweighted_minor"))
	}
	if weighted != wireInt(t, row, "weighted_minor") {
		t.Errorf("drill-through weighted sum %d != displayed %d", weighted, wireInt(t, row, "weighted_minor"))
	}
	// The server-side recompute over the same predicate set must agree too.
	for _, key := range []string{"deals", "unweighted_minor", "weighted_minor"} {
		if got, want := wireInt(t, derivation.Aggregates, key), wireInt(t, row, key); got != want {
			t.Errorf("recomputed aggregate %q = %d != displayed %d", key, got, want)
		}
	}
}

// The explanation rides the SAME row-scope clause as the report: a
// team-scoped rep's drill-through returns only their team's deals, and
// pointing the handle at a foreign owner yields an empty set, not a leak.
func TestForecastDerivationHonorsRowScope(t *testing.T) {
	e := setupForecast(t)
	e.seedOpenDeal(t, "Mine A", 20, &e.rep1, int64p(10000), stringp("commit"))
	e.seedOpenDeal(t, "Mine B", 60, &e.rep1, int64p(20000), stringp("commit"))
	e.seedOpenDeal(t, "Theirs", 20, &e.rep3, int64p(40000), stringp("commit"))

	rep := e.dealReadCtx(e.rep1, []ids.UUID{e.team1}, principal.RowScopeTeam)
	result := e.runForecast(t, rep, `{"group_by":["owner_id"]}`)
	if len(result.Rows) != 1 || result.Rows[0]["owner_id"] != e.rep1.String() {
		t.Fatalf("team-scoped report rows = %+v, want only rep1's group", result.Rows)
	}

	derivation := e.explain(t, rep, result.DerivationURL)
	if derivation.TotalRows != 2 || len(derivation.Rows) != 2 {
		t.Fatalf("team-scoped drill-through = %d rows (total %d), want rep1's 2 deals",
			len(derivation.Rows), derivation.TotalRows)
	}
	var sum int64
	for _, source := range derivation.Rows {
		sum += wireInt(t, source, "amount_minor")
	}
	if sum != 30000 {
		t.Errorf("team-scoped drill-through sum = %d, want 30000 (never the foreign 40000)", sum)
	}

	// A handle pinned to the foreign owner resolves to an EMPTY set
	// under team scope — anything that returns a record is a read.
	foreign := e.explain(t, rep, "/v1/reports/forecast/derivation?by=owner_id&agg=count%3A%3Adeals&owner_id="+e.rep3.String())
	if foreign.TotalRows != 0 || len(foreign.Rows) != 0 {
		t.Errorf("foreign-owner drill-through leaked %d rows (total %d)", len(foreign.Rows), foreign.TotalRows)
	}

	// Admin (row_scope=all) sees all three — the scope, not the data,
	// made the difference.
	full := e.explain(t, e.admin(), result.DerivationURL)
	if full.TotalRows != 3 {
		t.Errorf("admin drill-through total = %d, want 3", full.TotalRows)
	}
}
