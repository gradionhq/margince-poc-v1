// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// Migrate-once discipline for everything the integration lane compiles, as a
// fitness function. A suite migrates the schema once per test process
// (internal/platform/testdb.EnsureSchema) and resets between tests with a fast
// data-only reset (testdb.Reset); one that instead runs its own DROP SCHEMA +
// dbmigrate.Up on every setup reintroduces a per-test migrate that costs orders
// of magnitude more than the reset it replaces. The obligation is module-wide,
// so the walk is: a gate that judges one subtree also claims, silently, that the
// subtree is where the obligated code lives, and that second claim is the one
// nothing checks.
//
// Every file the lane COMPILES is judged, not only the _test.go ones: a shared
// fixture that moves into a build-tagged non-test file so sibling packages can
// import it is still the thing that migrates, and keying on the filename suffix
// would let it walk out of reach here while the gate kept passing. Membership is
// therefore the build tag, which is what actually decides whether the lane
// compiles a file.
//
// Any such file that REFERENCES the migrate entry point is caught, not only one
// that applies it, and detection is over the syntax tree rather than the text —
// both through gatekit.References, which carries the reasoning for each and is
// shared with the module-pool gate that needs exactly the same thing.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/gatekit"
)

// migrationsPackage owns the migrations themselves. Its suites drive them —
// applying, reversing, or upgrading from a pinned prefix — so migrating is the
// act under test rather than setup, and no shared pre-migrated schema can stand
// in for it. That is a different kind of thing from an exception, so it is
// excluded by rule rather than waived.
//
// The liveness check below is a floor, not a fence: it fires only if NO suite
// under migrations/ migrates any more, so the carve-out cannot outlive the
// directory. It would not notice one new suite there migrating per test for no
// reason. Real teeth would be per-file — every migrations/ test that calls Up
// must also call Down or load a pinned prefix — which is a bigger obligation
// than this gate owns today.
const migrationsPackage = "migrations"

// testdbPackage implements the migrate-once mechanism every suite is required to
// ride, so it is where dbmigrate.Up is SUPPOSED to be called. Excluded by rule
// rather than waived, for the same reason migrationsPackage is: a waiver would
// read as "this one is allowed to misbehave" when in fact it is the definition of
// behaving. Only reachable now that this gate walks non-test files too.
const testdbPackage = "internal/platform/testdb"

// inlineMigrators are the suites outside migrationsPackage ratified to migrate
// on their own, each bound to what the exception costs.
var inlineMigrators = gatekit.Waive(map[string]string{
	"internal/compose/integration/perfbench_integration_test.go": "seeds a large volume and asserts " +
		"query-latency SLOs against it, so it needs pristine physical tables — a reset cycle leaves bloat " +
		"and stale planner stats that move the very latencies under assertion. It migrates once for the " +
		"whole suite, so the cost it opts back into is negligible. It now carries `integration && bench` " +
		"and no merge gate runs it, but the waiver stays: this gate walks every _test.go file whatever tag " +
		"it carries, and the file still calls dbmigrate.Up.",
})

func TestIntegrationSuitesMigrateOncePerProcess(t *testing.T) {
	var offenders, inMigrations []string
	fset := token.NewFileSet()
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		// Every file the lane compiles, not only the _test.go ones. A shared
		// fixture that moves into a non-test file so sibling packages can import
		// it would otherwise walk straight out of this gate's reach while still
		// being the thing that migrates — and the gate would keep passing,
		// covering less, saying nothing. Membership is the build tag, which is
		// what actually decides whether the lane runs the file.
		if !strings.HasSuffix(path, "_test.go") && !isIntegrationTagged(path) {
			return nil
		}
		path = filepath.ToSlash(path)
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		if !referencesInlineMigrate(file) {
			return nil
		}
		if strings.HasPrefix(path, migrationsPackage+"/") {
			inMigrations = append(inMigrations, path)
			return nil
		}
		if strings.HasPrefix(path, testdbPackage+"/") {
			return nil
		}
		if !inlineMigrators.Waived(t, path) {
			offenders = append(offenders, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the module for integration-lane files: %v", err)
	}

	if len(offenders) > 0 {
		t.Errorf("%d integration-lane file(s) migrate inline instead of riding testdb.EnsureSchema — "+
			"a suite or a shared fixture in a build-tagged non-test file counts alike. Replace the "+
			"DROP SCHEMA + dbmigrate.Up block with testdb.EnsureSchema + testdb.Reset "+
			"(see internal/compose/integration/harness.go):\n\t%s",
			len(offenders), strings.Join(offenders, "\n\t"))
	}

	// The carve-out must stay live. If the migrations suites ever stop migrating,
	// the exclusion above becomes dead config that would silently re-admit an
	// inline migrator in that package (rule 2 — derive the obligation from the
	// system, do not maintain it as a list).
	if len(inMigrations) == 0 {
		t.Errorf("no suite under %s/ migrates any more — drop the carve-out (migrationsPackage), "+
			"it now only hides future inline migrators", migrationsPackage)
	}

	// And so must every waiver: one describing a file that no longer migrates is
	// a claim about code that is gone.
	inlineMigrators.AssertAllMatched(t)
}

// dbmigratePath is the migrate entry point's package, matched by import path
// rather than by the identifier a file happens to bind it to.
const dbmigratePath = "github.com/gradionhq/margince/backend/internal/platform/dbmigrate"

// referencesInlineMigrate reports whether the file reaches the migrate entry
// point at all — as a call, or as a value it could call later. gatekit.References
// carries the reasoning for treating a mere reference as a migration, and for
// resolving the qualifier through the file's own imports; this names WHAT is
// hunted, and nothing about how.
func referencesInlineMigrate(file *ast.File) bool {
	return gatekit.References(file, dbmigratePath, "Up")
}
