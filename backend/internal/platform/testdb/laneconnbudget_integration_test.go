// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package testdb_test

// The lane's connection budget, asserted against the cluster the lane is
// ACTUALLY running on rather than against the committed compose file.
//
// backend/laneconnbudget_test.go already gates the committed configuration in
// `make check`, and that is the gate a pull request meets. It cannot see the one
// failure mode that costs an afternoon: a container started before the compose
// file changed. Postgres applies max_connections at startup, so a cluster left
// up from an earlier checkout serves the old ceiling while every file in the
// tree says otherwise — the same shape as an api binary still serving :8080
// from a previous branch, and just as indistinguishable from a broken change.
//
// So this reads the live setting and the budget the lane computed for THIS run,
// and fails while the run is still explainable.
//
// Neither test skips. Every lane that runs integration tests declares the budget
// through scripts/lib-testdb.sh's declare_lane_budget — the parallel lane with
// its concurrency, the one-package and serial lanes with 1 — so an absent
// variable means the harness was reached by a route that did not size the
// cluster, and that is a setup failure of exactly the kind sharedAppPool already
// refuses for the DSNs. A skipped capacity check reads exactly like a passing
// one; the serial lane makes the point concrete by failing outright on any SKIP.

import (
	"context"
	"os"
	"strconv"
	"testing"

	"github.com/gradionhq/margince/backend/internal/platform/testdb"
)

// laneConnBudgetEnv carries the number scripts/test-integration-parallel.sh
// computed for this invocation: JOBS x (per-package + one admin connection) plus
// the lane's fixed cost. Absent outside that script — `make test-it` and a
// hand-run package oversubscribe nothing, and have no budget to check.
const laneConnBudgetEnv = "LANE_CONN_BUDGET"

func TestTheClusterSeatsTheBudgetTheLaneComputed(t *testing.T) {
	raw, ok := os.LookupEnv(laneConnBudgetEnv)
	if !ok || raw == "" {
		t.Fatalf("%s is unset — every lane declares it through scripts/lib-testdb.sh's declare_lane_budget (the parallel lane with its concurrency, the one-package and serial lanes with 1), so reaching this test without it means the harness was entered by a route that never sized the cluster", laneConnBudgetEnv)
	}
	budget, err := strconv.Atoi(raw)
	if err != nil || budget <= 0 {
		t.Fatalf("%s=%q is not a positive connection count; the lane computes it from its own terms, so a malformed value means the arithmetic there broke", laneConnBudgetEnv, raw)
	}

	pool := sharedAppPool(t)
	var maxConns int
	if err := pool.QueryRow(context.Background(),
		`SELECT current_setting('max_connections')::int`).Scan(&maxConns); err != nil {
		t.Fatalf("reading max_connections: %v", err)
	}

	if maxConns < budget {
		// Two causes produce this, and they need opposite actions — a message
		// that names only one sends half its readers to do something that
		// cannot help. Which one it is is decided by whether the budget was
		// computed for MORE concurrency than the committed configuration
		// provisions, and the run knows that: INTEGRATION_JOBS is the knob.
		remedy := "the CONTAINER predates the configuration — Postgres fixes max_connections at startup, so recreate it with " +
			"`docker compose -f infra/docker-compose.dev.yml up -d --force-recreate postgres` and re-run. " +
			"The committed compose file is separately checked by TestTheLaneFitsInsideTheClusterItRunsAgainst in `make check`, " +
			"so a green tree plus a short cluster is this case."
		if jobs := os.Getenv("INTEGRATION_JOBS"); jobs != "" {
			remedy = "this run set INTEGRATION_JOBS=" + jobs + ", so it budgeted for more concurrency than infra/docker-compose.dev.yml provisions. " +
				"Recreating the container CANNOT help — the compose file's max_connections is sized for the concurrency CI uses. " +
				"Either lower INTEGRATION_JOBS for this run, or raise max_connections and the terms in scripts/lib-testdb.sh together."
		}
		t.Fatalf("the cluster serves max_connections=%d and this lane run budgeted for %d.\n\n%s", maxConns, budget, remedy)
	}
}

// The ceiling the budget is computed from has to be the ceiling the pools
// actually take. It reaches this process as an environment variable, and every
// way that can silently fail — unexported by the lane, renamed on one side,
// parsed and dropped — leaves a pool at database.NewPool's own 16 with the lane
// still budgeting for 8. That is a lane whose demand is twice its own arithmetic
// and whose gate reads green.
func TestTheSharedPoolTakesTheCeilingTheLaneHandedIt(t *testing.T) {
	raw, ok := os.LookupEnv(testdb.PoolMaxConnsEnv)
	if !ok || raw == "" {
		t.Fatalf("%s is unset — declare_lane_budget exports it for every lane, so an absent ceiling means the pools are back on database.NewPool's fallback while the budget above still says otherwise", testdb.PoolMaxConnsEnv)
	}
	want, err := strconv.Atoi(raw)
	if err != nil || want <= 0 {
		t.Fatalf("%s=%q is not a positive connection count", testdb.PoolMaxConnsEnv, raw)
	}
	if got := sharedAppPool(t).Config().MaxConns; got != int32(want) {
		t.Fatalf("the shared pool's MaxConns is %d, want %d from %s — the lane budgeted for %d per pool and the pool is free to open %d, so the lane's whole connection arithmetic is out by the difference times INTEGRATION_JOBS",
			got, want, testdb.PoolMaxConnsEnv, want, got)
	}
}
