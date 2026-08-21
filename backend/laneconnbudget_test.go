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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

const (
	ciWorkflowPath = "../.github/workflows/ci.yml"
	// What the report calls the source of INTEGRATION_JOBS. It is read from the
	// caller AND the lanes it invokes, so naming one file would send the reader
	// to a file that may not hold the number.
	gateWorkflowLabel = "../.github/workflows/{ci,_lane-*}.yml"
	laneScriptPath    = "../scripts/lib-testdb.sh"
	composeInfraYML   = "../infra/docker-compose.dev.yml"
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

// gateWorkflows returns the merge gate as one body of text: the caller plus every
// lane workflow it invokes.
//
// The shard that sets INTEGRATION_JOBS moved out of ci.yml and into the
// integration lane. Reading the whole gate rather than one file of it means this
// arithmetic follows the declaration wherever it lives — the alternative is a
// gate that fails because the number it needs is one file over, which reads as a
// real finding and is not one.
func gateWorkflows(t *testing.T) string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(filepath.Dir(ciWorkflowPath), "ci.yml"))
	if err != nil {
		t.Fatalf("listing the gate caller: %v", err)
	}
	lanes, err := filepath.Glob(filepath.Join(filepath.Dir(ciWorkflowPath), "_lane-*.yml"))
	if err != nil {
		t.Fatalf("listing lane workflows: %v", err)
	}
	paths = append(paths, lanes...)
	if len(paths) == 0 {
		t.Fatalf("no merge-gate workflow found beside %s; this gate would read an empty string and its arithmetic would be silently smaller rather than wrong", ciWorkflowPath)
	}
	var joined strings.Builder
	for _, path := range paths {
		joined.WriteString(readRepoFile(t, path))
		joined.WriteString("\n")
	}
	return joined.String()
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
		t.Fatalf("the merge-gate workflows set no INTEGRATION_JOBS — this gate sizes the cluster for the concurrency CI actually uses, and cannot do that from a gate that no longer names it")
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		t.Fatalf("the merge-gate workflows set INTEGRATION_JOBS=%q, which is not a positive count", m[1])
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

// laneConnectionDemand asks the lane's own declaration for the number, by
// running declare_lane_budget the way every lane runs it.
//
// It deliberately does NOT re-implement `jobs * (perPackage + 1) + fixed`. A
// second spelling of one formula is the defect this whole gate is about: drop
// the `+1` in the shell and a Go copy would go on asserting the old arithmetic
// and passing, while the runtime guard in the integration lane compared against
// a different number. There is one expression, and this reads it.
//
// The shell is invoked rather than parsed for the same reason: a regex over the
// expression would be a third reading of it.
func laneConnectionDemand(t *testing.T, jobs int) int {
	t.Helper()
	out, err := exec.Command("bash", "-c",
		fmt.Sprintf("cd .. && source scripts/lib-testdb.sh && declare_lane_budget %d && printf %%s \"$LANE_CONN_BUDGET\"", jobs)).Output()
	if err != nil {
		t.Fatalf("asking %s for the budget at %d job(s): %v", laneScriptPath, jobs, err)
	}
	demand, convErr := strconv.Atoi(strings.TrimSpace(string(out)))
	if convErr != nil || demand <= 0 {
		t.Fatalf("%s's declare_lane_budget answered %q at %d job(s), which is not a positive connection count", laneScriptPath, out, jobs)
	}
	return demand
}

// fits is the comparison itself, named so that both directions of it can be
// exercised. Inline, it was reachable only through the happy path: a test that
// asserts "today's configuration passes" says nothing about whether the
// comparison still refuses anything.
func fits(demand, maxConns int) bool { return demand <= maxConns }

func TestTheLaneFitsInsideTheClusterItRunsAgainst(t *testing.T) {
	script := readRepoFile(t, laneScriptPath)
	jobs := ciIntegrationJobs(t, gateWorkflows(t))
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

	demand := laneConnectionDemand(t, jobs)
	if !fits(demand, maxConns) {
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
			jobs, gateWorkflowLabel,
			perPackage, laneScriptPath,
			fixed, laneScriptPath,
			demand, maxConns,
			composeInfraYML, demand)
	}
}

// The gate above is only a gate if it can fail, and there are two ways it can
// quietly stop being able to.
//
// The first is the arithmetic: it must still refuse a configuration this lane is
// known to outgrow — CI's 16 jobs against an unsized cluster's stock 100.
//
// The second is the COMPARISON, and it is the one an inline `if` hides. A test
// that only ever asserts "today's configuration passes" passes just as happily
// with the comparison inverted, at which point the gate refuses every correct
// configuration and accepts every broken one while reading green here. So both
// directions of `fits` are exercised by name.
func TestTheBudgetGateRefusesTheConfigurationThatShipped(t *testing.T) {
	const (
		jobsInCI      = 16  // .github/workflows/ci.yml, at the time of #1109
		stockMaxConns = 100 // what a Postgres with no max_connections serves
	)
	demand := laneConnectionDemand(t, jobsInCI)
	if fits(demand, stockMaxConns) {
		t.Fatalf("the budget arithmetic makes the pre-#1109 configuration fit (%d <= %d); the lane demanded that much against a stock cluster and this gate would have passed over it",
			demand, stockMaxConns)
	}
	if !fits(demand, demand) {
		t.Fatalf("fits() refuses a cluster sized exactly to the demand (%d), so the gate would fail a correct configuration", demand)
	}
	if !fits(demand, demand+1) {
		t.Fatalf("fits() refuses a cluster larger than the demand (%d against %d) — the comparison is inverted, and the gate now rejects every configuration that is big enough", demand, demand+1)
	}
	if fits(demand+1, demand) {
		t.Fatalf("fits() accepts a demand of %d against a cluster of %d — the comparison no longer refuses anything", demand+1, demand)
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
