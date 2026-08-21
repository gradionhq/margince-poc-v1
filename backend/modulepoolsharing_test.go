// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// Pool-sharing discipline for the module suites, as a fitness function.
//
// internal/platform/testdb.Pool hands a test PROCESS one pool per DSN, and the
// connections are the cost: a package's tests run sequentially against one clone
// database, so a pool opened per test dials backends, uses them once and closes
// them — while the lane runs several packages at once against ONE server. Every
// module suite used to do that, which is half of what #1744 measures: pools the
// lane's per-pool ceiling never reaches, so the connection budget declared in
// scripts/lib-testdb.sh describes a limit nothing enforces for them.
//
// The gate exists because the shape copies itself. A new suite is written by
// reading the nearest sibling, and nothing else would notice a per-test pool
// coming back — it costs time and connections, never a failure.
//
// SCOPED to internal/modules, and the narrowing is recorded rather than assumed.
// The compose suites take the shared pool through their own harness; the ones
// that ALSO build a pool of their own do it for reasons that differ per file (a
// second pool object on purpose, a pool pinned to one connection, the pool
// machinery's own tests), and #1744 carries them. A tier this gate does not
// judge is a tier that issue names. A MODULE file it does not judge would be a
// hole, which is what the liveness floor below is for.

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

// modulesTree is where this obligation lives — see the file comment for why it
// is not the whole module.
const modulesTree = "internal/modules"

// databasePath is the product pool constructor's package, pgxpoolPath is the
// driver's own, and testdbPath is the lane's shared one. All matched by import
// path rather than by whichever identifier a file binds them to.
const (
	databasePath = "github.com/gradionhq/margince/backend/internal/platform/database"
	pgxpoolPath  = "github.com/jackc/pgx/v5/pgxpool"
	testdbPath   = "github.com/gradionhq/margince/backend/internal/platform/testdb"
)

// poolConstructors is every way a file can obtain a pool of its own, and the
// list is the gate rather than a convenience.
//
// The product constructor alone was not enough, and that was not a hypothesis:
// the tree ALREADY held a module suite reaching the driver's own pool
// constructor directly (people/ensurechannel_contention, whose bound rides
// ConnConfig rather than the DSN), which a gate spelled against the product one
// read straight past. A gate that refuses one spelling of a mistake while a
// second spelling sits unjudged in the same tree is the shape this file exists
// to prevent, one level up.
//
// Named through the constants above rather than written out, and not only for
// tidiness: scripts/check-test-lanes.sh scans this tree as TEXT for the same
// constructors, to catch a unit test opening real infrastructure. Spelling one
// in prose here makes this gate its own offender — which it duly did.
var poolConstructors = []struct{ pkg, symbol string }{
	{databasePath, "NewPool"},
	{pgxpoolPath, "New"},
	{pgxpoolPath, "NewWithConfig"},
}

// ownPools ratifies module suites that build a pool of their own, each bound to
// what its exception costs.
//
// Per FILE and per reason, never per category. "A suite may open a pool when it
// needs different connection parameters" would admit every call site this gate
// exists to refuse, because a per-test pool on the lane's own app DSN is exactly
// what the next suite would claim needs its own parameters.
var ownPools = gatekit.Waive(map[string]string{
	"internal/modules/identity/oauth_lend_lock_integration_test.go": "builds its pool on a DERIVED DSN — " +
		"the lane's own plus lock_timeout — so that a contended row lock decides the outcome instead of the " +
		"clock. It is a per-test instrument, bound to one test's transaction and closed with it; sharing it " +
		"would apply that timeout to every other test in the package, and testdb.Pool keys by DSN so it " +
		"would be a second shared pool rather than the lane's.",
	"internal/modules/people/ensurechannel_contention_integration_test.go": "the same instrument one module " +
		"over, built through pgxpool directly because the bound rides ConnConfig rather than the DSN: a " +
		"lock_timeout of 250ms so that a contended account lock decides the outcome instead of the clock. " +
		"Per-test and closed with the test, and it re-registers the typed ids the product pool would have.",
})

// TestModuleSuitesTakeTheProcessSharedPool fails when a module integration test
// builds its own pool instead of taking testdb's.
func TestModuleSuitesTakeTheProcessSharedPool(t *testing.T) {
	var offenders, sharing, unguarded []string
	fset := token.NewFileSet()
	err := filepath.WalkDir(modulesTree, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, "_integration_test.go") {
			return err
		}
		path = filepath.ToSlash(path)
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		if gatekit.References(file, testdbPath, "Pool") {
			sharing = append(sharing, path)
			// Taking the shared pool without the quiescence gate is the SECOND
			// half of this obligation and it fails quietly. Closing the pool per
			// test used to break a goroutine a test left running; a shared pool
			// has no such moment, so the straggler writes into the database the
			// NEXT test just reset and the wrong suite reports it.
			if !gatekit.References(file, testdbPath, "AssertPoolsQuiesced") {
				unguarded = append(unguarded, path)
			}
		}
		if !opensItsOwnPool(file) {
			return nil
		}
		if !ownPools.Waived(t, path) {
			offenders = append(offenders, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s for integration suites: %v", modulesTree, err)
	}

	if len(offenders) > 0 {
		t.Errorf("%d module integration suite(s) build their own pool instead of taking testdb.Pool — "+
			"a pool per test dials connections, uses them once and closes them, and stays outside the "+
			"per-pool ceiling the lane budgets for (#1744). Call testdb.EnsureSchema and then "+
			"testdb.Pool, and register testdb.AssertPoolsQuiesced where the pool is handed out "+
			"(see internal/modules/people/dedupe_integration_test.go):\n\t%s",
			len(offenders), strings.Join(offenders, "\n\t"))
	}

	if len(unguarded) > 0 {
		t.Errorf("%d module integration suite(s) take testdb.Pool without registering "+
			"testdb.AssertPoolsQuiesced — register it where the pool is handed out, before the test adds "+
			"any cleanup of its own, so it runs last (t.Cleanup is LIFO) and sees a package that has "+
			"genuinely stopped:\n\t%s",
			len(unguarded), strings.Join(unguarded, "\n\t"))
	}

	// The floor. A gate whose subjects have all moved out of its tree reports
	// nothing and reads exactly like a clean one, and this gate's tree is the
	// one thing it asserts silently: that module suites are where the shared
	// pool is taken. If none of them take it any more, the walk is judging a
	// tree the obligation has left.
	if len(sharing) == 0 {
		t.Errorf("no suite under %s/ takes testdb.Pool — either the module tier stopped running against "+
			"a real database, or this gate is walking the wrong tree; it is certifying nothing either way",
			modulesTree)
	}

	// And the waiver must stay live: one describing a file that no longer builds
	// its own pool is a claim about code that is gone.
	ownPools.AssertAllMatched(t)
}

// opensItsOwnPool reports whether a file builds a pool of its own, by any of the
// spellings available to it.
func opensItsOwnPool(file *ast.File) bool {
	for _, c := range poolConstructors {
		if gatekit.References(file, c.pkg, c.symbol) {
			return true
		}
	}
	return false
}
