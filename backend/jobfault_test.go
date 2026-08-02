// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// Every River worker returns through jobs.Fault. River stores whatever a
// Work method returns into river_job.errors verbatim — a column with no
// workspace, no RLS, and a fleet-wide audience. A worker that returns its
// raw cause publishes it there; this gate is what stops the next worker
// from doing so by habit.
//
// The rule is syntactic on purpose: a return in a Work body is nil, a
// jobs.Fault call, or one of River's own control returns. Anything else
// fails, which keeps the gate readable and impossible to argue with.

import (
	"go/ast"
	"path/filepath"
	"testing"
)

// workerFloor guards against a vacuous pass, as in the role gate.
const workerFloor = 20

// nilAfterLogging are the ratified workers that log a failure and return nil.
// That shape is EXACTLY the defect this phase removes — a tenant failure
// becoming a green River row — so each entry states the durable retry policy
// that makes it honest here. A worker not listed must return its failure.
var nilAfterLogging = map[string]string{
	"captureSyncWorker":               "the connector sidecar owns the retry: a failed sync leaves next_sync_at unadvanced, so the dispatcher re-enqueues it on the next scan — the job row's success means 'this attempt is concluded', not 'the sync succeeded'",
	"captureBackfillWorker":           "the backfill ROW owns the outcome: RunBackfillStep ends the run and records the fault class on the row against its own give-up cap, on a context detached from the job because the job context dying mid-page is the commonest fault. A River retry would re-page a run the engine already ended",
	"overlayReconcileWorkspaceWorker": "the only nil-after-logging path is the disconnect fence: every fenced write aborted with ErrConnectionGone, so there is nothing to retry and nothing to back off. A genuine sweep failure IS returned, and its backoff recorded",
	"voiceBuildWorker":                "the build ROW owns its state: every model failure lands on the row as deferred or failed, never as a River retry loop, and the deferred-retry sweep re-enqueues what is due",
}

func TestEveryWorkerReturnsThroughJobsFault(t *testing.T) {
	fset, files := parseGoFilesUnder(t, filepath.Join("internal", "compose"))
	workers := 0
	usedNilWaiver := map[string]bool{}
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Name.Name != "Work" || fn.Body == nil {
				continue
			}
			workers++
			for _, ret := range topLevelReturns(fn.Body) {
				for _, result := range ret.Results {
					if sanctionedWorkerReturn(result) {
						continue
					}
					pos := fset.Position(result.Pos())
					t.Errorf("%s:%d: a worker return must be nil, jobs.Fault(...), or a river control return — a raw cause is written verbatim into river_job.errors",
						pos.Filename, pos.Line)
				}
			}
			// A named error result reaches river_job.errors without passing
			// through any return statement: siteDeepReadWorker.Work declares
			// (workErr error) and its panic-recovery defer assigns it
			// directly (deepreadbudget.go). Checking returns alone would
			// wave that path through, so every assignment to the named
			// result takes the same test.
			for _, assigned := range namedResultAssignments(fn) {
				if sanctionedWorkerReturn(assigned) {
					continue
				}
				pos := fset.Position(assigned.Pos())
				t.Errorf("%s:%d: an assignment to a worker's named error result must be nil, jobs.Fault(...), or a river control return — it reaches river_job.errors exactly as a return does",
					pos.Filename, pos.Line)
			}
			// The log-and-return-nil shape is the defect this phase removes,
			// one level up: the tenant failed, the operator sees a green row.
			// A worker that error-logs AND returns nil is that shape unless a
			// durable retry policy elsewhere makes the success honest.
			recv := receiverTypeName(fn)
			if errorLogsAndReturnsNil(fn) {
				if _, waived := nilAfterLogging[recv]; waived {
					usedNilWaiver[recv] = true
				} else {
					pos := fset.Position(fn.Pos())
					t.Errorf("%s:%d: %s logs an error and returns nil — River will record this job as completed while the work failed. Return the failure, or ratify it in nilAfterLogging naming the retry policy that makes success honest.",
						pos.Filename, pos.Line, recv)
				}
			}
		}
	}
	if workers < workerFloor {
		t.Fatalf("found only %d Work methods, expected at least %d — the walker matched nothing", workers, workerFloor)
	}
	for recv, reason := range nilAfterLogging {
		if reason == "" {
			t.Errorf("%s: nil-after-logging waiver without a rationale", recv)
		}
		if !usedNilWaiver[recv] {
			t.Errorf("%s: stale waiver — it no longer logs-and-returns-nil; delete it", recv)
		}
	}
}

// errorLogsAndReturnsNil reports whether fn both logs a failure and returns
// nil. Warn counts as well as Error: the defect is the SHAPE — a tenant's
// failure becoming a green River row — and the level a worker happened to log
// it at does not change what the operator sees in the job list.
// A heuristic, and deliberately a broad one: the cost of a false positive is
// writing one waiver with a rationale, while the cost of a false negative is
// a tenant failure that never surfaces anywhere.
func errorLogsAndReturnsNil(fn *ast.FuncDecl) bool {
	logs, returnsNil := false, false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.CallExpr:
			if sel, ok := v.Fun.(*ast.SelectorExpr); ok {
				switch sel.Sel.Name {
				case "Error", "ErrorContext", "Warn", "WarnContext":
					logs = true
				}
			}
		case *ast.ReturnStmt:
			for _, r := range v.Results {
				if ident, ok := r.(*ast.Ident); ok && ident.Name == "nil" {
					returnsNil = true
				}
			}
		}
		return true
	})
	return logs && returnsNil
}

// topLevelReturns collects the return statements belonging to fn itself,
// descending through control flow but NOT into nested function literals —
// a closure passed to a helper has its own contract and returns to its own
// caller, not to River.
func topLevelReturns(body *ast.BlockStmt) []*ast.ReturnStmt {
	var found []*ast.ReturnStmt
	ast.Inspect(body, func(node ast.Node) bool {
		switch v := node.(type) {
		case *ast.FuncLit:
			return false
		case *ast.ReturnStmt:
			found = append(found, v)
			return false
		}
		return true
	})
	return found
}

// namedResultAssignments returns every value assigned to fn's named error
// result. A Work method with unnamed results has none, and the defer that
// assigns one is reached through a FuncLit — which topLevelReturns
// deliberately skips — so this walk descends into literals rather than
// stopping at them.
func namedResultAssignments(fn *ast.FuncDecl) []ast.Expr {
	if fn.Type.Results == nil {
		return nil
	}
	named := map[string]bool{}
	for _, result := range fn.Type.Results.List {
		for _, name := range result.Names {
			if name.Name != "_" {
				named[name.Name] = true
			}
		}
	}
	if len(named) == 0 {
		return nil
	}
	var assigned []ast.Expr
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		stmt, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, lhs := range stmt.Lhs {
			ident, ok := lhs.(*ast.Ident)
			if !ok || !named[ident.Name] || i >= len(stmt.Rhs) {
				continue
			}
			assigned = append(assigned, stmt.Rhs[i])
		}
		return true
	})
	return assigned
}

// sanctionedWorkerReturn reports whether one returned expression is an
// allowed worker result.
func sanctionedWorkerReturn(expr ast.Expr) bool {
	if ident, ok := expr.(*ast.Ident); ok && ident.Name == "nil" {
		return true
	}
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	if pkg.Name == "jobs" && (sel.Sel.Name == "Fault" || sel.Sel.Name == "FaultContext") {
		return true
	}
	// River's own control returns are not failures: a snooze reschedules and
	// a cancel is a deliberate stop. Neither carries a cause to publish.
	return pkg.Name == "river" && (sel.Sel.Name == "JobSnooze" || sel.Sel.Name == "JobCancel")
}
