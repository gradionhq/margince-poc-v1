// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// Migrate-once discipline for the compose/integration suites as a fitness
// function. The package migrates the schema exactly once per test process
// (internal/platform/testdb.EnsureSchema) and resets between tests with a fast
// TRUNCATE (testdb.Truncate); a suite that instead runs its own DROP SCHEMA +
// dbmigrate.Up on every setup silently reintroduces the ~0.8s-per-test migrate
// that once dominated the lane. The obligation is derived from the tree — any
// new *_test.go in the package that calls dbmigrate.Up is caught here — so the
// pattern cannot creep back one copy-pasted setup at a time.
//
// perfbench is the one sanctioned exception: it seeds a large volume and asserts
// query-latency SLOs, so it wants pristine physical tables (no bloat or stale
// planner stats from prior TRUNCATE cycles) and pays a genuine fresh migrate. It
// runs once, so the cost it opts back into is negligible.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The directory whose integration suites must ride the shared migrate-once
// harness, and the one file allowed to migrate inline within it.
const (
	integrationSuiteDir    = "internal/compose/integration"
	perfbenchExceptionFile = "perfbench_integration_test.go"
)

func TestComposeIntegrationSuitesMigrateOncePerProcess(t *testing.T) {
	entries, err := os.ReadDir(integrationSuiteDir)
	if err != nil {
		t.Fatalf("reading %s: %v", integrationSuiteDir, err)
	}
	var offenders []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, "_test.go") || name == perfbenchExceptionFile {
			continue
		}
		path := filepath.Join(integrationSuiteDir, name)
		b, err := os.ReadFile(path) // #nosec G304 -- path is a *_test.go file from the trusted source tree
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		// dbmigrate.Up is the migrate entry point; a call to it (open paren,
		// never prose) in a suite setup is an inline per-test migration.
		if strings.Contains(string(b), "dbmigrate.Up(") {
			offenders = append(offenders, name)
		}
	}
	if len(offenders) > 0 {
		t.Errorf("%d compose/integration suite(s) migrate inline instead of riding testdb.EnsureSchema — "+
			"replace the DROP SCHEMA + dbmigrate.Up block with testdb.EnsureSchema + testdb.Truncate (see harness.go):\n\t%s",
			len(offenders), strings.Join(offenders, "\n\t"))
	}

	// The allowlist must stay live: if perfbench is ever converted to the
	// shared harness too, its carve-out here becomes dead config that would
	// silently re-admit an inline migrator. Fail so the exception is removed
	// rather than left to rot (rule 2 — derive the obligation from the system).
	perfbench, err := os.ReadFile(filepath.Join(integrationSuiteDir, perfbenchExceptionFile)) // #nosec G304 -- fixed in-tree source path
	if err != nil {
		t.Fatalf("reading %s: %v", perfbenchExceptionFile, err)
	}
	if !strings.Contains(string(perfbench), "dbmigrate.Up(") {
		t.Errorf("%s no longer migrates inline — drop it from the migrate-once allowlist (perfbenchExceptionFile)", perfbenchExceptionFile)
	}
}
