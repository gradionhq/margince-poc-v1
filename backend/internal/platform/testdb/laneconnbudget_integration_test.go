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
// Both tests below skip when their variable is absent, and that skip cannot hide
// anything: the parallel lane exports both unconditionally, and it FAILS ON ANY
// SKIP. So inside the lane the skip path is unreachable by construction, and
// outside it — `make test-it`, one package by hand — there is no product of
// concurrent packages to be over budget in the first place.

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
		t.Skipf("%s is unset — this package is not running under the parallel lane, which is the only caller whose demand is a product", laneConnBudgetEnv)
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
		t.Fatalf(`the cluster serves max_connections=%d and this lane run budgeted for %d.

The committed infra/docker-compose.dev.yml is checked by
TestTheLaneFitsInsideTheClusterItRunsAgainst in `+"`make check`"+`, so a green tree
and a short cluster together mean the CONTAINER predates the configuration:
Postgres fixes max_connections at startup. Recreate it — `+"`make db-up`"+` after a
`+"`docker compose -f infra/docker-compose.dev.yml up -d --force-recreate postgres`"+`
— rather than lowering the budget to fit what is running.`, maxConns, budget)
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
		t.Skipf("%s is unset — no ceiling was declared for this run, so the pool correctly keeps database.NewPool's fallback", testdb.PoolMaxConnsEnv)
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
