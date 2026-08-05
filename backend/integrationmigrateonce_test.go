// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// Migrate-once discipline for every integration suite in the module, as a
// fitness function. A suite migrates the schema once per test process
// (internal/platform/testdb.EnsureSchema) and resets between tests with a fast
// data-only reset (testdb.Reset); one that instead runs its own DROP SCHEMA +
// dbmigrate.Up on every setup reintroduces a per-test migrate that costs orders
// of magnitude more than the reset it replaces. The obligation is module-wide,
// so the walk is: a gate that judges one subtree also claims, silently, that the
// subtree is where the obligated code lives, and that second claim is the one
// nothing checks.
//
// What is caught: a file that imports .../dbmigrate and applies .Up. What is
// not: a caller inside package dbmigrate itself, and one reaching Up through a
// function value. Resolving those would mean loading the module through
// go/types for a case no suite has ever written — the boundary is stated here
// rather than papered over with a claim of totality.
//
// Detection is by call site, not by text: the string "dbmigrate.Up(" appears in
// this file's own report, and a text scan over a tree that includes this file
// would flag the gate as its own offender.

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
		if err != nil || d.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return err
		}
		path = filepath.ToSlash(path)
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		if !callsInlineMigrate(file) {
			return nil
		}
		if strings.HasPrefix(path, migrationsPackage+"/") {
			inMigrations = append(inMigrations, path)
			return nil
		}
		if !inlineMigrators.Waived(t, path) {
			offenders = append(offenders, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the module for test files: %v", err)
	}

	if len(offenders) > 0 {
		t.Errorf("%d integration suite(s) migrate inline instead of riding testdb.EnsureSchema — "+
			"replace the DROP SCHEMA + dbmigrate.Up block with testdb.EnsureSchema + testdb.Reset "+
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

// callsInlineMigrate reports whether the file calls dbmigrate.Up — the migrate
// entry point. A call, not a mention: the selector has to be applied, so the
// function named in a comment or a report string is not a migration.
//
// The qualifier is resolved through the file's own imports, because the name is
// the caller's choice: `import dm ".../dbmigrate"` then `dm.Up(...)`, or a
// dot-import then a bare `Up(...)`, migrate exactly as much as the canonical
// spelling does. Comparing the identifier alone would let a rename walk past the
// one gate this whole lane's cost model rests on.
func callsInlineMigrate(file *ast.File) bool {
	qualifier, dotImported := dbmigrateName(file)
	if qualifier == "" && !dotImported {
		return false
	}
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.SelectorExpr:
			if fun.Sel == nil || fun.Sel.Name != "Up" || qualifier == "" {
				return true
			}
			if pkg, ok := fun.X.(*ast.Ident); ok && pkg.Name == qualifier {
				found = true
				return false
			}
		case *ast.Ident:
			// Only reachable under a dot-import, where Up is unqualified.
			if dotImported && fun.Name == "Up" {
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
