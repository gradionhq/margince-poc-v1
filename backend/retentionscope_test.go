// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// retentionScopeBuilder is the fixture whose reach this gate bounds, and
// retentionScopeSink is the one call it may feed.
const (
	retentionScopeBuilder = "RetentionPassCtx"
	retentionScopeSink    = "EvaluateWorkspace"
)

// TestRetentionPassCtxOnlyDrivesTheRetentionPass keeps a context that cannot be
// denied from becoming the subject of an assertion about denial.
//
// integration.RetentionPassCtx binds a SYSTEM principal, because that is the
// provenance the retention worker actually writes rows under and a suite binding
// only the workspace would exercise a pass production never runs. But
// auth.Unbounded reports true for a system principal, and auth.Require and
// auth.EnsureVisible short-circuit for it — so a visibility or row-scope
// assertion made THROUGH this context passes whatever the row scope is. Such a
// test looks exactly like the gate it is impersonating.
//
// The fixture is exported (a sibling suite package needs it), which is what puts
// it within reach of every suite in the tree rather than the one file that
// declared it. So the bound is enforced here instead of asked for in a comment:
// it may be passed to the retention engine and nowhere else. A second legitimate
// sink is a deliberate act — add it to this gate and say why.
func TestRetentionPassCtxOnlyDrivesTheRetentionPass(t *testing.T) {
	fset := token.NewFileSet()
	checked := 0
	err := filepath.WalkDir("internal", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		// Every call whose argument list contains a RetentionPassCtx call is a
		// legitimate sink; anything else that calls it is not passing the scope
		// straight into a pass.
		sinks := map[ast.Node]bool{}
		ast.Inspect(file, func(n ast.Node) bool {
			outer, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			for _, arg := range outer.Args {
				if inner, ok := arg.(*ast.CallExpr); ok && callsNamed(inner, retentionScopeBuilder) {
					sinks[inner] = calleeName(outer) == retentionScopeSink
				}
			}
			return true
		})
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !callsNamed(call, retentionScopeBuilder) {
				return true
			}
			checked++
			if !sinks[call] {
				t.Errorf("%s: %s is used somewhere other than an argument to %s — a system principal cannot be denied, so any visibility or row-scope claim made through it holds vacuously",
					fset.Position(call.Pos()), retentionScopeBuilder, retentionScopeSink)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walking internal: %v", err)
	}
	// A gate that matched nothing proves nothing: the fixture was renamed, or the
	// walk stopped reaching the suites that use it.
	if checked == 0 {
		t.Fatalf("no call to %s anywhere under internal/ — this gate has stopped watching what it names", retentionScopeBuilder)
	}
}

// callsNamed reports whether call invokes a function named name, as either a
// bare identifier (in-package) or a package selector (from a sibling).
func callsNamed(call *ast.CallExpr, name string) bool {
	return calleeName(call) == name
}

// calleeName is the called function's own name, ignoring any qualifier.
func calleeName(call *ast.CallExpr) string {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return fun.Name
	case *ast.SelectorExpr:
		return fun.Sel.Name
	}
	return ""
}
