// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

// The one structural fact about this role's boot that a wiring change can break
// silently: WHERE the extension tier's per-call Runtime is bound.
//
// It is asserted over the source rather than by booting a worker because the
// failure it guards is a POSITION, not a value. The regression it exists to
// prevent already happened once: the only binding sat inside startRunnerLane,
// after `if modelPath.AgentLoop == nil { return nil }`, so a worker with no
// model configured never bound — and that worker still runs the job lane, whose
// composed extension ticks reach the installation through exactly that handle.
// Nothing failed loudly: every capability answers errExtensionRuntimeUnwired, a
// clean refusal, so the symptom is a scheduled job that refuses forever.
//
// A behavioural test cannot see this. compose exposes BindExtensionRuntime and
// no reader, so a booted worker cannot be asked what it bound, and the guard
// that broke it is a plain early return that any future refactor could
// reintroduce. What the boot CAN be asked is whether the call is on the job
// lane's path at all, unconditionally — which is the property.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// bindCall is the selector this file is about.
const bindPkg, bindFn = "compose", "BindExtensionRuntime"

// bindingSites reports, per enclosing function in this package, how many
// compose.BindExtensionRuntime calls it makes and how many of those sit inside
// a conditional or a loop body.
func bindingSites(t *testing.T) (calls map[string]int, guarded map[string]int) {
	t.Helper()
	fset := token.NewFileSet()
	// Every .go file in this directory, parsed one by one rather than through
	// parser.ParseDir (deprecated, and it would associate files by package
	// without honouring build tags — this role has tagged files).
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading cmd/worker: %v", err)
	}
	calls, guarded = map[string]int{}, map[string]int{}
	scanned := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		file, err := parser.ParseFile(fset, entry.Name(), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parsing %s: %v", entry.Name(), err)
		}
		scanned++
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			// depth counts the enclosing branch/loop nodes on the way down,
			// so a call the walk reaches at depth 0 is one the function
			// always makes.
			var walk func(n ast.Node, depth int)
			walk = func(n ast.Node, depth int) {
				if n == nil {
					return
				}
				switch node := n.(type) {
				case *ast.CallExpr:
					if isBindCall(node) {
						calls[fn.Name.Name]++
						if depth > 0 {
							guarded[fn.Name.Name]++
						}
					}
				case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt,
					*ast.TypeSwitchStmt, *ast.SelectStmt, *ast.FuncLit:
					for _, child := range children(node) {
						walk(child, depth+1)
					}
					return
				}
				for _, child := range children(n) {
					walk(child, depth)
				}
			}
			walk(fn.Body, 0)
		}
	}
	// A walk that matched nothing would make every assertion below pass
	// vacuously, which is the one way a fitness test fails silently.
	if scanned < 2 {
		t.Fatalf("scanned only %d Go file(s) in cmd/worker — the walk matched almost nothing", scanned)
	}
	return calls, guarded
}

// children lists a node's direct children through ast.Inspect on each field,
// which is simpler than enumerating every node type this walk can meet.
func children(n ast.Node) []ast.Node {
	var out []ast.Node
	ast.Inspect(n, func(c ast.Node) bool {
		if c == nil || c == n {
			return c == n
		}
		out = append(out, c)
		return false
	})
	return out
}

func isBindCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != bindFn {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == bindPkg
}

// TestTheJobLaneBindsTheExtensionRuntimeUnconditionally is the regression.
//
// startJobRunner is the job lane's only door — it is what builds and starts the
// River runner every worker runs, model or no model — so the binding has to be
// there, and it has to be reached on every path through it. A guard of any kind
// around this call is the exact shape that left the lane unbound before.
func TestTheJobLaneBindsTheExtensionRuntimeUnconditionally(t *testing.T) {
	calls, guarded := bindingSites(t)
	if calls["startJobRunner"] == 0 {
		t.Fatalf("startJobRunner does not bind the extension runtime — a worker with no model configured then runs composed extension jobs against an unbound process, and every tick refuses with errExtensionRuntimeUnwired forever")
	}
	if guarded["startJobRunner"] > 0 {
		t.Fatalf("startJobRunner's %s call sits behind a conditional — the job lane runs on every worker, so a guarded binding is the regression this test exists for", bindFn)
	}
}

// TestTheRunnerLaneStillBindsToo: the Surface-B lane binds as well, so a run
// resumed before the job runner is wired is not left refusing during that
// window. It is allowed to be guarded — it sits after the AgentLoop return, and
// that is fine now that it is not the only site.
func TestTheRunnerLaneStillBindsToo(t *testing.T) {
	calls, _ := bindingSites(t)
	if calls["startRunnerLane"] == 0 {
		t.Fatal("startRunnerLane no longer binds the extension runtime — a Surface-B run resumed before startJobRunner would refuse every extension tool call")
	}
}

// TestTheRoleBindsFromExactlyTheTwoKnownSites: a third site would be a third
// opinion about the pool and the custodian, and BindExtensionRuntime only warns
// on a rebind to a different POOL — a site passing a different vault would
// change what every extension secret read resolves against, silently, depending
// on which lane bound last.
func TestTheRoleBindsFromExactlyTheTwoKnownSites(t *testing.T) {
	calls, _ := bindingSites(t)
	known := map[string]bool{"startJobRunner": true, "startRunnerLane": true}
	for fn, n := range calls {
		if !known[fn] {
			t.Errorf("%s makes %d %s call(s) — the role binds from startJobRunner and startRunnerLane, and both pass the same two values on purpose", fn, n, bindFn)
		}
		if n > 1 {
			t.Errorf("%s binds %d times", fn, n)
		}
	}
}
