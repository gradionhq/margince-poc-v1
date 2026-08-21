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
// drops the cache, and any function that asks pg_blocking_pids must call it.
//
// THREE things are required, not one, because co-occurrence is not the
// invariant. The clear must run BEFORE the probe, on the SAME receiver, and that
// receiver must not be a pool:
//
//   - a clear issued after the probe, or in a branch the run never takes, leaves
//     the probe reading the snapshot it was meant to drop;
//   - a clear on a different connection drops a different connection's snapshot;
//   - a POOL is the case that looks right and is not. pool.Exec(clear) acquires a
//     connection, runs, and releases it, so the pool.QueryRow(probe) that follows
//     may be handed another one entirely and the clear provably cannot bind.
//     Acquire once and issue both on that connection.
//
// The first version of this gate required only that both strings appear
// somewhere in the same function, and it certified a pool call site as
// protected. A gate that reads green over the defect it names is the thing this
// file exists to prevent.
//
// Scoped to pg_blocking_pids rather than to pg_stat_activity generally: counting
// sessions by role or database is a census, not a race, and a stale answer there
// is not a blind one.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/shared/gatekit"
)

const (
	// blockingPidsProbe is what makes a query a contention probe: it asks who is
	// waiting on whom, which is the question a stale snapshot answers wrongly.
	blockingPidsProbe = "pg_blocking_pids"

	// snapshotClear is the call that makes the next read on that connection live.
	snapshotClear = "pg_stat_clear_snapshot"

	// pooledReceiver marks a handle that hands out a connection per call. Matched
	// case-insensitively on the receiver's source text, which is conservative by
	// construction: it can only ever demand that a call site be more explicit
	// about which connection it is on.
	pooledReceiver = "pool"
)

// probeTrees are the hand-written Go trees this repo ships. Each is its own
// module, which is why the list is not `./...`: a suite added under extensions/
// opens the same database and must inherit this obligation the day it is
// written, exactly as the license header gate enumerates the same three.
var probeTrees = []string{".", "../extensions", "../fixtures"}

// sqlCall is one database call in a function: what it executes, where, and on
// which receiver.
type sqlCall struct {
	receiver string
	pos      token.Pos
}

// TestEveryContentionProbeClearsTheStatsSnapshot fails on a function that asks
// pg_blocking_pids without a snapshot clear that can actually reach it.
func TestEveryContentionProbeClearsTheStatsSnapshot(t *testing.T) {
	var offenders []string
	fset := token.NewFileSet()

	for _, tree := range probeTrees {
		if _, err := os.Stat(tree); err != nil {
			// A tree the vanilla checkout does not ship is not an exemption for
			// anything, because nothing in it can hold a probe.
			continue
		}
		found, err := scanTree(tree, fset)
		if err != nil {
			t.Fatalf("scanning %s for contention probes: %v", tree, err)
		}
		offenders = append(offenders, found...)
	}
	sort.Strings(offenders)

	if len(offenders) > 0 {
		t.Fatalf(`%d contention probe(s) are not protected by a %s() the probe can see:

  %s

pg_stat_activity's row set is materialized once per transaction and cached until
it ends, so a probe issued inside one cannot see a backend that dialled after the
snapshot was taken — at any budget, on any machine (#970). Whether a connection
is inside a transaction is a property of the CALL SITE and not of the probe: in
approvals it looked exempt because the field is a bare *pgx.Conn, and competingTx
opens a transaction on that same connection.

The clear must run BEFORE the probe, on the SAME receiver, and that receiver must
not be a pool — a pool hands out a connection per call, so a clear issued through
one cannot bind the connection the probe is then given. Acquire a connection and
issue both statements on it.`,
			len(offenders), snapshotClear, strings.Join(offenders, "\n  "))
	}
}

// scanTree parses every Go file under root and reports each unprotected probe.
func scanTree(root string, fset *token.FileSet) ([]string, error) {
	var offenders []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if skipDir(entry.Name()) {
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
			for _, reason := range unprotectedProbes(fset, fn) {
				offenders = append(offenders, fmt.Sprintf("%s: %s — %s", path, fn.Name.Name, reason))
			}
		}
		return nil
	})
	return offenders, err
}

// skipDir reports whether a directory holds no compiled call site.
//
// `testdata` and the `_`/`.` prefixes are the GO TOOLCHAIN's own exclusions: it
// never builds them, so nothing in one can be a probe this gate is responsible
// for — and a deliberately unparsable fixture in there would otherwise take the
// whole gate red for a file that is not code. That is a scoping rule, not the
// "skip what you cannot read" hole this file refuses elsewhere: a parse failure
// inside a COMPILED tree still fails, because that is a member of the census.
//
// vendor and node_modules are dependency trees; build is generated.
func skipDir(name string) bool {
	switch name {
	case "vendor", "node_modules", "build", "testdata":
		return true
	}
	return strings.HasPrefix(name, "_") || strings.HasPrefix(name, ".")
}

// unprotectedProbes reports, for each probe in fn, why it is not protected —
// naming the failure rather than returning a boolean, because "no clear at all",
// "cleared afterwards" and "cleared through a pool" need different fixes.
func unprotectedProbes(fset *token.FileSet, fn *ast.FuncDecl) []string {
	probes, clears := collectSQLCalls(fset, fn)
	var reasons []string
	for _, probe := range probes {
		switch {
		case len(clears) == 0:
			reasons = append(reasons, "asks "+blockingPidsProbe+" and never calls "+snapshotClear+"()")
		case strings.Contains(strings.ToLower(probe.receiver), pooledReceiver):
			reasons = append(reasons, "probes through `"+probe.receiver+"`, a pool: each call may be handed a different connection, so no clear can bind this probe")
		case !clearedBefore(clears, probe):
			reasons = append(reasons, "asks "+blockingPidsProbe+" on `"+probe.receiver+"` with no "+snapshotClear+"() on that receiver before it")
		}
	}
	return reasons
}

// clearedBefore reports whether some clear runs earlier than the probe on the
// same receiver.
func clearedBefore(clears []sqlCall, probe sqlCall) bool {
	for _, clear := range clears {
		if clear.receiver == probe.receiver && clear.pos < probe.pos {
			return true
		}
	}
	return false
}

// collectSQLCalls finds the calls in fn whose arguments carry the probe or the
// clear, and records the receiver each is issued on.
//
// Read from the argument literals rather than from the call graph: the query
// text is where both facts live, and a probe assembled through a helper this
// gate could not follow would read as absent — a census that passes by seeing
// nothing.
func collectSQLCalls(fset *token.FileSet, fn *ast.FuncDecl) (probes, clears []sqlCall) {
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		call, isCall := node.(*ast.CallExpr)
		if !isCall {
			return true
		}
		selector, isSelector := call.Fun.(*ast.SelectorExpr)
		if !isSelector {
			return true
		}
		found := sqlCall{receiver: exprText(fset, selector.X), pos: call.Pos()}
		for _, arg := range call.Args {
			text, ok := gatekit.LiteralText(arg)
			if !ok {
				continue
			}
			if strings.Contains(text, blockingPidsProbe) {
				probes = append(probes, found)
			}
			if strings.Contains(text, snapshotClear) {
				clears = append(clears, found)
			}
		}
		return true
	})
	return probes, clears
}

// exprText renders a receiver expression back to source, so `e.Pool` and
// `conn` are compared as a reader compares them.
func exprText(fset *token.FileSet, expr ast.Expr) string {
	var out strings.Builder
	if err := printer.Fprint(&out, fset, expr); err != nil {
		// Unprintable receivers are not silently treated as matching anything:
		// a unique marker can equal no other receiver, so the probe reads as
		// uncleared rather than as cleared.
		return fmt.Sprintf("<unprintable receiver at %d>", expr.Pos())
	}
	return out.String()
}
