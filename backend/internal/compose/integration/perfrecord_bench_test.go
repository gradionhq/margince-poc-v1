// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration && bench

package integration

// The PERF-1 / PERF-4 record benchmarks: how long the SERVER takes to open a
// record and to save one, measured at the transport the number is published
// about.
//
// Two deliberate differences from perfbench_integration_test.go next door,
// which measures PERF-3/PERF-7:
//
//   - It measures through the BOOTED APPLICATION, not the store. PERF-3 and
//     PERF-7 are query budgets ("search", "context-graph assembly") and a store
//     call is the honest unit for them. PERF-1 and PERF-4 are budgets on an
//     OPERATION a person performs, and their column says "server" — routing,
//     admission, the RLS transaction and serialization are all inside the number
//     a customer is quoted, so measuring underneath them would report a figure
//     nobody promised.
//   - It carries the `bench` tag, so the merge gate never RUNS it. `make vet`
//     still type-checks this lane, which is the only reason a file no test lane
//     compiles does not rot.
//
// Run it with `make bench-record`.

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/compose/integration/apptest"
	"github.com/gradionhq/margince/backend/internal/modules/search"
)

// The published budgets (acceptance-standards.md §Parameters — performance
// budgets). They are calibration values per ADR-0021 §5: changing one is a
// noted budget revision, never a silent bump to make a red run green.
const (
	// perf1RecordOpenBudget bounds opening a person, organization or deal.
	// PERF-1's other half — 300 ms PERCEIVED — is a browser measurement and
	// belongs to the throttled mobile profile (MOBILE-AC-2), not here.
	perf1RecordOpenBudget = 100 * time.Millisecond
	// perf4RecordSaveBudget bounds a save/mutation (PERF-4).
	perf4RecordSaveBudget = 150 * time.Millisecond
)

// recordBenchSpec sizes the measurement loop. benchRuns reads only warmups and
// sample off a spec — the seeding fields belong to the volume-tier benchmark
// next door and are left zero here on purpose, because this suite seeds through
// its own path rather than seedBenchTier's.
//
// More samples than the tier harness takes: an HTTP round trip is cheaper than
// a 250k-row graph walk, so the runs are affordable, and a p95 over 30 samples
// is read off the 29th value rather than the 19th.
var recordBenchSpec = benchTierSpec{tier: search.BenchTierSMB, warmups: 5, sample: 30}

// The background volume the measured record sits in. A record open against an
// empty database measures nothing anybody will experience: the planner picks
// different paths at three rows than at ten thousand, and an index that is never
// exercised looks exactly as fast as one that does not exist.
const (
	recordBenchPersons       = 10_000
	recordBenchOrganizations = 1_000
	recordBenchActivities    = 20_000
)

func TestRecordOpenAndSaveBudgets(t *testing.T) {
	e := apptest.SetupApp(t)
	e.BootstrapWorkspace(t)
	seedRecordBenchVolume(t, e)

	person := createBenchPerson(t, e)
	organization, deal := createBenchOrganizationAndDeal(t, e)

	report := search.BenchReport{Tier: recordBenchSpec.tier, Queries: []search.QueryStats{
		benchRecordOpen(t, e, "record_open_person", "/v1/people/"+person),
		benchRecordOpen(t, e, "record_open_organization", "/v1/organizations/"+organization),
		benchRecordOpen(t, e, "record_open_deal", "/v1/deals/"+deal),
		benchRecordSave(t, e, person),
	}}

	for _, q := range report.Queries {
		t.Logf("perfbench [%s]: %s p50=%s p95=%s p99=%s (budget %s, %d samples)",
			report.Tier, q.Query, q.P50, q.P95, q.P99, q.Budget, q.Samples)
	}
	// The record is written BEFORE the gate, deliberately. A breach is exactly
	// the run whose numbers a reader most wants to see, and writing after the
	// gate would keep the page green while the build went red.
	writeRecordBenchRecord(t, e, report.Queries)
	if err := report.Gate(); err != nil {
		t.Fatalf("PERF-1/PERF-4 budget gate is red: %v", err)
	}
}

// writeRecordBenchRecord leaves this run's numbers on disk for the published
// page. The PERF id is carried per row rather than per file because one target
// measures two different published budgets — open is PERF-1, save is PERF-4 —
// and a page that grouped them under one id would misattribute both.
func writeRecordBenchRecord(t *testing.T, e *apptest.AppEnv, queries []search.QueryStats) {
	t.Helper()
	measurements := make([]BudgetMeasurement, 0, len(queries))
	for _, q := range queries {
		id := "PERF-1"
		if q.Query == "record_save_person" {
			id = "PERF-4"
		}
		measurements = append(measurements,
			MeasurementFrom(id, q.Query, q.P50, q.P95, q.P99, q.Budget, q.Samples))
	}
	WritePerfRecord(t, "bench-record", benchPostgresVersion(e.Owner), measurements)
}

// benchPostgresVersion asks the server under measurement what it is. A latency
// is a claim about that server as much as about this code, so the record says
// which one answered.
func benchPostgresVersion(owner *pgx.Conn) string {
	return PostgresVersion(func(sql string) (string, error) {
		var version string
		err := owner.QueryRow(context.Background(), sql).Scan(&version)
		return version, err
	})
}

// benchRecordOpen measures one GET the record page issues (PERF-1). A non-200
// fails the run rather than being recorded: a 404 answered quickly is not a
// record open, and averaging it in would report a budget as held by the one
// response that does no work.
func benchRecordOpen(t *testing.T, e *apptest.AppEnv, name, path string) search.QueryStats {
	t.Helper()
	stats, err := benchRuns(name, perf1RecordOpenBudget, recordBenchSpec, func() error {
		return benchExpectOK(t, e, http.MethodGet, path, nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	return stats
}

// benchRecordSave measures the PATCH a record edit issues (PERF-4). The body
// changes the title on every run so no run can be answered by a no-op write —
// a save that stores nothing is not the operation the budget is about.
func benchRecordSave(t *testing.T, e *apptest.AppEnv, personID string) search.QueryStats {
	t.Helper()
	edit := 0
	stats, err := benchRuns("record_save_person", perf4RecordSaveBudget, recordBenchSpec, func() error {
		edit++
		return benchExpectOK(t, e, http.MethodPatch, "/v1/people/"+personID,
			apptest.AnyMap{"title": "Rear Admiral " + strconv.Itoa(edit)})
	})
	if err != nil {
		t.Fatal(err)
	}
	return stats
}

// seedRecordBenchVolume bulk-loads background rows into the bootstrapped
// organization's workspace through the owner connection, set-based.
//
// The GUC is set inside the transaction because the seeded rows take their
// workspace from it — an unbound INSERT here does not fail loudly, it writes
// rows the workspace-predicated reads under benchmark will never return, and
// the benchmark would then measure an empty table while reporting a seeded
// one.
func seedRecordBenchVolume(t *testing.T, e *apptest.AppEnv) {
	t.Helper()
	ctx := context.Background()

	var workspace string
	if err := e.Owner.QueryRow(ctx, `SELECT id FROM workspace WHERE slug = $1`, e.Slug).Scan(&workspace); err != nil {
		t.Fatalf("resolving the bootstrapped workspace: %v", err)
	}
	tx, err := e.Owner.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	//craft:ignore swallowed-errors error-path safety net only — the Commit below is asserted, after which this rollback is a designed no-op
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT set_config('app.workspace_id', $1, true)`, workspace); err != nil {
		t.Fatalf("binding the workspace GUC: %v", err)
	}
	for _, seed := range recordBenchSeeds(workspace) {
		if _, err := tx.Exec(ctx, seed.sql, seed.args...); err != nil {
			t.Fatalf("seeding %s: %v", seed.what, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// recordBenchSeeds is the seeding program, one statement per row kind. It is a
// list rather than a run of Exec calls so the failure above can name WHICH kind
// failed — a bare "seeding: constraint violated" over four statements sends the
// reader to read all four.
func recordBenchSeeds(workspace string) []struct {
	what string
	sql  string
	args []any
} {
	return []struct {
		what string
		sql  string
		args []any
	}{
		{"persons", `INSERT INTO person (full_name, source, captured_by)
		   SELECT 'Bench Person ' || i, 'manual', 'human:bench'
		   FROM generate_series(1, $1) AS i`, []any{recordBenchPersons}},
		{"organizations", `INSERT INTO organization (display_name, source, captured_by)
		   SELECT 'Bench Org ' || i, 'manual', 'human:bench'
		   FROM generate_series(1, $1) AS i`, []any{recordBenchOrganizations}},
		{"activities", `INSERT INTO activity (kind, subject, body, occurred_at, source, captured_by)
		   SELECT 'email', 'Bench subject ' || i, 'Bench body ' || i,
		          now() - (i % 720 || ' hours')::interval, 'manual', 'human:bench'
		   FROM generate_series(1, $1) AS i`, []any{recordBenchActivities}},
		// The planner chooses differently against stale statistics, and a
		// benchmark that measures the plan for an empty table is measuring the
		// fixture rather than the product.
		{"statistics", `ANALYZE person, organization, activity`, nil},
	}
}

// createBenchPerson creates the measured person through the real endpoint, so
// the row the benchmark opens is one the product's own writer produced.
func createBenchPerson(t *testing.T, e *apptest.AppEnv) string {
	t.Helper()
	var person apptest.AnyMap
	if status := e.Call(t, http.MethodPost, "/v1/people", apptest.AnyMap{
		"full_name": "Grace Hopper",
		"source":    "ui",
		"emails":    []apptest.AnyMap{{"email": "grace@navy.mil", "is_primary": true}},
	}, nil, &person); status != http.StatusCreated {
		t.Fatalf("create person = %d %v", status, person)
	}
	return benchID(t, person, "person")
}

// createBenchOrganizationAndDeal creates the other two records PERF-1 names.
// The deal needs the organization, so the two are made together rather than
// through two fixtures that would each have to make one.
func createBenchOrganizationAndDeal(t *testing.T, e *apptest.AppEnv) (string, string) {
	t.Helper()
	stages := apptest.DiscoverSeededPipeline(t, e)

	var org apptest.AnyMap
	if status := e.Call(t, http.MethodPost, "/v1/organizations", apptest.AnyMap{
		"display_name": "Acme GmbH",
		"source":       "ui",
		"domains":      []apptest.AnyMap{{"domain": "acme.example", "is_primary": true}},
	}, nil, &org); status != http.StatusCreated {
		t.Fatalf("create organization = %d %v", status, org)
	}
	orgID := benchID(t, org, "organization")

	var deal apptest.AnyMap
	if status := e.Call(t, http.MethodPost, "/v1/deals", apptest.AnyMap{
		"name": "Acme rollout", "amount_minor": 250_000_00, "currency": "EUR",
		"pipeline_id": stages.PipelineID, "stage_id": stages.Open,
		"organization_id": orgID, "source": "ui",
	}, nil, &deal); status != http.StatusCreated {
		t.Fatalf("create deal = %d %v", status, deal)
	}
	return orgID, benchID(t, deal, "deal")
}

// benchID reads the created record's id, failing on a payload that carries none
// rather than measuring requests against an empty path.
func benchID(t *testing.T, record apptest.AnyMap, what string) string {
	t.Helper()
	id, ok := record["id"].(string)
	if !ok {
		t.Fatalf("the created %s carries no string id: %v", what, record)
	}
	return id
}

// benchExpectOK issues one measured request and turns a non-200 into the error
// benchRuns reports, so a broken fixture reads as a broken fixture rather than
// as a very fast budget. The error names the call and what the server said
// about it, because "run 7 failed" sends the reader nowhere.
//
// reqBody stays a nil interface when there is no body: a typed nil map handed
// to Call is not nil to it, and would be marshalled as a literal `null`.
func benchExpectOK(t *testing.T, e *apptest.AppEnv, method, path string, body apptest.AnyMap) error {
	t.Helper()
	var reqBody any
	if body != nil {
		reqBody = body
	}
	var out apptest.AnyMap
	if status := e.Call(t, method, path, reqBody, nil, &out); status != http.StatusOK {
		return fmt.Errorf("%s %s → %d %v", method, path, status, out)
	}
	return nil
}
