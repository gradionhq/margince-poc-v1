// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// The integration lane's connection demand is a PRODUCT — concurrent packages
// times what one package may hold — and for most of this repo's life neither
// factor knew about the third number it had to fit inside. INTEGRATION_JOBS was
// raised to 16 in CI, database.NewPool's fallback ceiling was 16 per pool, and
// the compose Postgres never had max_connections set at all, so the lane ran
// against the stock 100 with a ceiling of 256 for its shared pools alone.
//
// Nothing failed reliably, which is the whole difficulty: MaxConns is a ceiling
// and not a reservation, so pgxpool dials lazily and whether a run fits is
// decided by how the bursts happen to overlap. #1109 is what that looks like
// from outside — connect-time failures naming a DIFFERENT package set every
// run, green in isolation, green at INTEGRATION_JOBS=3.
//
// So the obligation is derived from the three files that declare the factors,
// never restated here: a term this test hardcoded would be a fourth number free
// to drift from the other three, which is the defect rather than the gate. What
// IS asserted here is the relation between them.
//
// The mirror matters as much as the sum. A gate that only checks "the terms fit"
// reads green when a term goes missing — an INTEGRATION_JOBS the workflow no
// longer sets, or a max_connections dropped back out of the compose command,
// both leave a smaller number on the left-hand side and pass. Each term is
// therefore required to be PRESENT and non-zero before the arithmetic runs.

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const (
	ciWorkflowPath  = "../.github/workflows/ci.yml"
	laneScriptPath  = "../scripts/test-integration-parallel.sh"
	composeInfraYML = "../infra/docker-compose.dev.yml"
)

// laneTerm reads one `NAME=<int>` assignment from the lane script. Anchored to
// the line start so a mention inside a comment or a message cannot answer for
// the declaration.
func laneTerm(t *testing.T, script, name string) int {
	t.Helper()
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(name) + `=(\d+)$`)
	m := re.FindStringSubmatch(script)
	if m == nil {
		t.Fatalf("%s declares no %s= — the lane's connection budget is one of its terms, and a missing term makes this gate's arithmetic silently smaller rather than wrong", laneScriptPath, name)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		t.Fatalf("%s sets %s=%q, which is not a positive count", laneScriptPath, name, m[1])
	}
	return n
}

// ciIntegrationJobs reads the INTEGRATION_JOBS the integration shard runs with.
// It is read from the workflow rather than from the script's nproc-derived
// default because CI is the environment that oversubscribes: the default is
// min(nproc, 8) and the shard deliberately runs 16 on a 4-core runner.
func ciIntegrationJobs(t *testing.T, workflow string) int {
	t.Helper()
	re := regexp.MustCompile(`(?m)^\s*INTEGRATION_JOBS:\s*(\d+)\s*$`)
	m := re.FindStringSubmatch(workflow)
	if m == nil {
		t.Fatalf("%s sets no INTEGRATION_JOBS — this gate sizes the cluster for the concurrency CI actually uses, and cannot do that from a workflow that no longer names it", ciWorkflowPath)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		t.Fatalf("%s sets INTEGRATION_JOBS=%q, which is not a positive count", ciWorkflowPath, m[1])
	}
	return n
}

// composeMaxConnections reads the server ceiling out of the postgres service's
// own command line. Read from the `-c name=value` argument rather than from any
// mention of the word, so the comment that explains the number cannot answer for
// the number.
func composeMaxConnections(t *testing.T, compose string) int {
	t.Helper()
	re := regexp.MustCompile(`(?m)^\s*-\s*max_connections=(\d+)\s*$`)
	m := re.FindStringSubmatch(compose)
	if m == nil {
		t.Fatalf("%s passes no `-c max_connections=…` to postgres, so the lane runs against the stock 100 — which is the state #1109 reported", composeInfraYML)
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		t.Fatalf("%s sets max_connections=%q, which is not a positive count", composeInfraYML, m[1])
	}
	return n
}

func readRepoFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(b)
}

// laneConnectionDemand is the arithmetic itself, kept in one place so the test
// below and its own defect test cannot disagree about what is being asserted.
//
// The `+1` per job is the admin connection a slot can be holding for a clone
// CREATE/DROP at the same moment its package is at its own ceiling: cmd/migrate's
// db verbs open one connection each, not a pool, and at a handover JOBS of them
// overlap.
//
// Spelled "one connection" rather than naming the pgx constructor on purpose:
// scripts/check-test-lanes.sh greps untagged test files for the real-infra
// constructors and does not strip comments first, so the token alone in this
// prose reported this file as a unit test opening a real database (#1741).
func laneConnectionDemand(jobs, perPackage, fixed int) int {
	return jobs*(perPackage+1) + fixed
}

func TestTheLaneFitsInsideTheClusterItRunsAgainst(t *testing.T) {
	script := readRepoFile(t, laneScriptPath)
	jobs := ciIntegrationJobs(t, readRepoFile(t, ciWorkflowPath))
	perPool := laneTerm(t, script, "LANE_POOL_MAX_CONNS")
	perPackage := laneTerm(t, script, "LANE_CONNS_PER_PACKAGE")
	fixed := laneTerm(t, script, "LANE_FIXED_CONNS")
	maxConns := composeMaxConnections(t, readRepoFile(t, composeInfraYML))

	// A per-package allowance below a single pool's ceiling is not a budget: one
	// pool alone could spend it, and the second pool testdb opens would then be
	// over before any test ran.
	if perPackage < perPool {
		t.Fatalf("LANE_CONNS_PER_PACKAGE=%d is below LANE_POOL_MAX_CONNS=%d — a package opens more than one pool from these DSNs, so its allowance cannot be smaller than one pool's ceiling", perPackage, perPool)
	}

	demand := laneConnectionDemand(jobs, perPackage, fixed)
	if demand > maxConns {
		t.Fatalf(`the integration lane can demand %d connections and %s allows %d.

    INTEGRATION_JOBS                 %3d   %s
  x LANE_CONNS_PER_PACKAGE           %3d   %s
  + one admin connection per slot      1   CREATE/DROP DATABASE at a handover
  + LANE_FIXED_CONNS                 %3d   %s
  ----------------------------------------
                                     %3d   demanded
                                     %3d   max_connections

Raise max_connections in %s to at least %d, or lower a term. Do not leave them
apart: they were unrelated numbers once, and the lane failed at connect time in a
different package set every run (#1109).`,
			demand, composeInfraYML, maxConns,
			jobs, ciWorkflowPath,
			perPackage, laneScriptPath,
			fixed, laneScriptPath,
			demand, maxConns,
			composeInfraYML, demand)
	}
}

// The gate above is only a gate if it can fail, and the failure it must catch is
// the configuration this repo actually shipped: CI's 16 jobs against a stock
// cluster nobody had sized. This runs the same arithmetic over that
// configuration and asserts it does NOT fit — so a future edit that makes the
// relation vacuous (a term dropped, a comparison inverted) is caught by the same
// file that states it.
func TestTheBudgetGateRefusesTheConfigurationThatShipped(t *testing.T) {
	const (
		jobsInCI          = 16  // .github/workflows/ci.yml, at the time of #1109
		poolCeilingPerPkg = 32  // two pools at database.NewPool's un-pinned 16
		stockMaxConns     = 100 // what a Postgres with no max_connections serves
	)
	script := readRepoFile(t, laneScriptPath)
	fixed := laneTerm(t, script, "LANE_FIXED_CONNS")

	if demand := laneConnectionDemand(jobsInCI, poolCeilingPerPkg, fixed); demand <= stockMaxConns {
		t.Fatalf("the budget arithmetic makes the pre-#1109 configuration fit (%d <= %d); it demanded %d against a stock cluster and this gate would have passed over it",
			demand, stockMaxConns, demand)
	}
}

// The pinned ceiling is what stops the demand growing again behind the gate: a
// harness that never receives the ceiling falls back to database.NewPool's 16
// however small LANE_POOL_MAX_CONNS is, and the arithmetic above would go on
// budgeting for a limit nothing applies. Both ends of that seam are asserted,
// because either one alone can be present while the pin does nothing.
func TestTheLanePinsThePoolCeilingItBudgetsFor(t *testing.T) {
	script := readRepoFile(t, laneScriptPath)
	// The lane's end: the ceiling is EXPORTED, since the workers re-exec and a
	// variable that is set but not exported expands to empty in them.
	if !strings.Contains(script, `export MARGINCE_TEST_POOL_MAX_CONNS="$LANE_POOL_MAX_CONNS"`) {
		t.Fatalf("%s no longer exports MARGINCE_TEST_POOL_MAX_CONNS from LANE_POOL_MAX_CONNS — the budget above would be sized for a ceiling the harness never hears about", laneScriptPath)
	}
	// The harness's end: the name the lane exports is the name testdb reads.
	pool := readRepoFile(t, "../backend/internal/platform/testdb/pool.go")
	if !strings.Contains(pool, `PoolMaxConnsEnv = "MARGINCE_TEST_POOL_MAX_CONNS"`) {
		t.Fatal("backend/internal/platform/testdb/pool.go no longer declares PoolMaxConnsEnv as MARGINCE_TEST_POOL_MAX_CONNS — the two ends of this seam are joined by that string alone, and a rename on one side is silent")
	}
	if !strings.Contains(pool, `params["pool_max_conns"]`) {
		t.Fatal("backend/internal/platform/testdb/pool.go reads the ceiling but no longer applies it as pool_max_conns — a pool that ignores the lane's ceiling makes the budget fiction")
	}
}
