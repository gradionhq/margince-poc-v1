// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// Migrate-once discipline for every integration suite in the module, as a
// fitness function. A suite migrates the schema once per test process
// (internal/platform/testdb.EnsureSchema) and resets between tests with a fast
// data-only reset (testdb.Reset); one that instead runs its own DROP SCHEMA +
// dbmigrate.Up on every setup reintroduces the per-test migrate that once
// dominated the lane. The obligation is derived from the tree — any *_test.go
// that calls dbmigrate.Up is caught — so the pattern cannot creep back one
// copy-pasted setup at a time.
//
// This gate used to walk internal/compose/integration alone. Inside that
// directory it held; outside it, three suites migrated per test unpoliced, and
// the cost it was written to prevent had grown from the ~0.8s/test it names to
// ~3s. A gate that judges one subtree also claims, silently, that the subtree is
// where the obligated code lives, and only the first claim was ever checked. The
// obligation is module-wide, so the walk is now module-wide and there is no root
// left to be wrong about.
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
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/gatekit"
)

// migrationsPackage owns the migrations themselves. Its suites apply, reverse
// and reapply them, so migrating is the act under test rather than setup, and no
// shared pre-migrated schema can stand in for it. That is a different kind of
// thing from an exception, so it is excluded by rule rather than waived — and
// the exclusion is proven live below, so it cannot outlast the suites it exists
// for.
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
		if path == migrationsPackage || strings.HasPrefix(path, migrationsPackage+"/") {
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

// callsInlineMigrate reports whether the file calls dbmigrate.Up — the migrate
// entry point. A call, not a mention: the selector has to be applied, so the
// function named in a comment or a report string is not a migration.
func callsInlineMigrate(file *ast.File) bool {
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil || sel.Sel.Name != "Up" {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if ok && pkg.Name == "dbmigrate" {
			found = true
			return false
		}
		return true
	})
	return found
}
