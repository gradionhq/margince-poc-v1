// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// A contention probe that cannot see the backend it is waiting for.
//
// pg_stat_activity is not a live view. Its row set is materialized once per
// TRANSACTION and cached until that transaction ends, so a probe issued inside
// one is answering from a snapshot taken before the racer it is watching for
// existed. A racer whose pooled connection is dialled mid-race is then invisible
// FOREVER — at any budget, on any machine. That is #970, and it cost two issues
// (#548, #516) that read as timeouts and were not.
//
// The trap is that this is a property of the CALL SITE, never of the probe's own
// code, and the obvious reading of a call site is wrong. In approvals,
// waitForRowLockWaiter looked exempt because e.owner is a bare *pgx.Conn — but
// competingTx opens a transaction ON THAT SAME CONNECTION, so pgx ran every
// probe inside it. Nothing in the probe's file says so.
//
// So the obligation is derived rather than remembered: pg_stat_clear_snapshot()
// drops the cache, and any function that asks pg_blocking_pids must call it. It
// is cheap, it is a no-op where the snapshot was already fresh, and making it
// unconditional is what removes the question a reviewer would otherwise have to
// re-answer at every new call site. A test added next month that passes a
// transaction-bound connection reintroduces #970 silently; this is what stops it.
//
// Scoped to pg_blocking_pids and not to pg_stat_activity generally: counting
// sessions by role or database (extmigrategate) is a census, not a race, and a
// stale answer there is not a blind one.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const (
	// blockingPidsProbe is what makes a query a contention probe: it asks who is
	// waiting on whom, which is the question the stale snapshot answers wrongly.
	blockingPidsProbe = "pg_blocking_pids"

	// snapshotClear is the call that makes the next read live.
	snapshotClear = "pg_stat_clear_snapshot"
)

// TestEveryContentionProbeClearsTheStatsSnapshot walks every Go tree in this
// module and fails on a function that asks pg_blocking_pids without first
// dropping the transaction's cached view of who is connected.
func TestEveryContentionProbeClearsTheStatsSnapshot(t *testing.T) {
	var offenders []string
	fset := token.NewFileSet()

	walkErr := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			// Vendored and generated trees are nobody's call site.
			if name := entry.Name(); name == "vendor" || name == "node_modules" || name == "build" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		file, parseErr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if parseErr != nil {
			// Not skipped. A file this gate cannot read is a file it cannot
			// clear, and a census that quietly drops its unreadable members
			// reports "no offenders" for a tree it never saw.
			return fmt.Errorf("parsing %s: %w", path, parseErr)
		}
		for _, decl := range file.Decls {
			fn, isFunc := decl.(*ast.FuncDecl)
			if !isFunc || fn.Body == nil {
				continue
			}
			probes, clears := scanForProbe(fn)
			if probes && !clears {
				offenders = append(offenders, path+": "+fn.Name.Name)
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walking the module for contention probes: %v", walkErr)
	}

	if len(offenders) > 0 {
		t.Fatalf(`%d contention probe(s) ask %s without calling %s() first:

  %s

pg_stat_activity's row set is materialized once per transaction and cached until
it ends, so a probe issued inside one cannot see a backend that dialled after the
snapshot was taken — at any budget, on any machine (#970). Whether the connection
is inside a transaction is a property of the CALL SITE and not of the probe: in
approvals it looked exempt because the field is a bare *pgx.Conn, and competingTx
opens a transaction on that same connection.

Add `+"`"+`SELECT `+snapshotClear+`()`+"`"+` immediately before the probe. It is cheap, and where
the snapshot was already fresh it does nothing.`,
			len(offenders), blockingPidsProbe, snapshotClear, strings.Join(offenders, "\n  "))
	}
}

// scanForProbe reports whether a function's string literals contain a
// contention probe and whether they contain the snapshot clear.
//
// Read from the literals rather than from the call graph on purpose: the query
// text is where both facts actually live, and a probe assembled from a helper
// this gate could not follow would read as absent — a census that passes by
// seeing nothing.
func scanForProbe(fn *ast.FuncDecl) (probes, clears bool) {
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		lit, isLit := node.(*ast.BasicLit)
		if !isLit || lit.Kind != token.STRING {
			return true
		}
		value, err := strconv.Unquote(lit.Value)
		if err != nil {
			value = lit.Value
		}
		if strings.Contains(value, blockingPidsProbe) {
			probes = true
		}
		if strings.Contains(value, snapshotClear) {
			clears = true
		}
		return true
	})
	return probes, clears
}
