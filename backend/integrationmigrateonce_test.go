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
// that applies it: `up := dm.Up; up(...)` migrates just as much as `dm.Up(...)`,
// and a file has no reason to hold the migrator except to run it. That closes the
// function-value route without type-aware analysis, and the qualifier is resolved
// through imports so a rename or dot-import cannot walk past it either.
//
// Detection is over the syntax tree, not the text: the string "dbmigrate.Up"
// appears in this file's own doc and failure message, and a text scan over a tree
// that includes this file would flag the gate as its own offender.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
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
		"whole suite, so the cost it opts back into is negligible.",
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
// point at all — as a call, or as a value it could call later.
//
// Naming it is enough, deliberately. A gate that matched only `dm.Up(...)` would
// be walked past by `up := dm.Up; up(...)`, and following a function value needs
// type-aware analysis of the whole module. Treating any reference as a migration
// closes that without it: a test file has no reason to hold the migrator except
// to run it, and a suite that genuinely needs one says so through a waiver rather
// than through a spelling the gate cannot see.
//
// The qualifier is resolved through the file's own imports, because the name is
// the caller's choice — `import dm ".../dbmigrate"`, or a dot-import leaving `Up`
// bare, migrate exactly as much as the canonical spelling does. A test inside
// package dbmigrate itself reaches `Up` with no import at all, so that case is
// keyed on the package clause.
//
// Prose is not a reference: the strings "dbmigrate.Up" in this file's own doc and
// failure message are comments and literals, which carry no selector for the AST
// to match.
func referencesInlineMigrate(file *ast.File) bool {
	qualifier, dotImported := dbmigrateName(file)
	inPackage := file.Name != nil && file.Name.Name == "dbmigrate"
	if qualifier == "" && !dotImported && !inPackage {
		return false
	}
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		if found {
			return false
		}
		switch node := n.(type) {
		case *ast.SelectorExpr:
			// qualifier.Up, whether or not it is being applied here.
			if qualifier == "" || node.Sel == nil || node.Sel.Name != "Up" {
				return true
			}
			if pkg, ok := node.X.(*ast.Ident); ok && pkg.Name == qualifier {
				found = true
				return false
			}
		case *ast.Ident:
			// A bare Up: reachable under a dot-import, or from inside the
			// migrator's own package.
			if (dotImported || inPackage) && node.Name == "Up" {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

// dbmigrateName returns the identifier this file binds the migrate package to,
// and whether it was dot-imported. Both are empty/false when the file does not
// import it at all, which is the cheap way to skip most of the tree.
func dbmigrateName(file *ast.File) (qualifier string, dotImported bool) {
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil || path != dbmigratePath {
			continue
		}
		switch {
		case spec.Name == nil:
			return "dbmigrate", false
		case spec.Name.Name == ".":
			return "", true
		case spec.Name.Name == "_":
			// Imported for side effects only; it cannot be called through.
			return "", false
		default:
			return spec.Name.Name, false
		}
	}
	return "", false
}
