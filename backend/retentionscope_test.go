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

// retentionScopeBuilder is the fixture whose reach these gates bound,
// retentionScopeSink is the one call it may feed, and retentionScopeSinkOwner is
// the package that call must live in.
const (
	retentionScopeBuilder   = "RetentionPassCtx"
	retentionScopeSink      = "EvaluateWorkspace"
	retentionScopeSinkOwner = "internal/modules/privacy"
)

// TestRetentionPassCtxOnlyDrivesTheRetentionPass keeps a context that cannot be
// denied from becoming the subject of an assertion about denial.
//
// integration.RetentionPassCtx binds a SYSTEM principal, because that is the
// provenance the retention worker actually writes rows under. But auth.Unbounded
// reports true for a system principal, and auth.Require and auth.EnsureVisible
// short-circuit for it — so a visibility or row-scope assertion made THROUGH this
// context passes whatever the row scope is. Such a test looks exactly like the
// gate it is impersonating.
//
// The fixture is exported, which is what puts it in reach of every suite in the
// tree rather than the one file that declared it. So the bound is enforced here:
// it may be passed to the retention engine and nowhere else.
//
// It matches every REFERENCE, not every call, and that distinction is the whole
// gate. `passCtx := integration.RetentionPassCtx` followed by `passCtx(ws)` binds
// the same undeniable principal as the direct spelling, and a gate watching only
// CallExpr nodes would not see it — worse, it would not count it either, so the
// liveness check below would stay satisfied by the honest call sites while the
// aliased one went unexamined. backend/integrationmigrateonce_test.go bounds its
// own entry point the same way and for the same reason.

func TestRetentionPassCtxOnlyDrivesTheRetentionPass(t *testing.T) {
	fset := token.NewFileSet()
	references := 0
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
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
		// The two shapes that are not findings, collected first: the builder called
		// directly in the sink's argument list, and the builder's own declaration,
		// which is where the name comes from rather than a use of it.
		sanctioned := map[ast.Node]bool{}
		ast.Inspect(file, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.FuncDecl:
				if node.Name != nil && node.Name.Name == retentionScopeBuilder {
					sanctioned[node.Name] = true
				}
			case *ast.CallExpr:
				if calleeName(node) != retentionScopeSink {
					return true
				}
				for _, arg := range node.Args {
					inner, ok := arg.(*ast.CallExpr)
					if ok && calleeName(inner) == retentionScopeBuilder {
						sanctioned[inner.Fun] = true
					}
				}
			}
			return true
		})
		ast.Inspect(file, func(n ast.Node) bool {
			if !namesRetentionScopeBuilder(n) {
				return true
			}
			references++
			if !sanctioned[n] {
				t.Errorf("%s: %s is referenced somewhere other than directly as an argument to %s — a system principal cannot be denied, so any visibility or row-scope claim made through it holds vacuously. Inline the call into the %s argument; if a second sink is genuinely right, add it to retentionScopeSink here and say why",
					fset.Position(n.Pos()), retentionScopeBuilder, retentionScopeSink, retentionScopeSink)
			}
			// A matched node is the whole reference; its children are the
			// qualifier and the name, neither of which is another reference.
			return false
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walking the module: %v", err)
	}
	// A gate that matched nothing proves nothing: the fixture was renamed, or the
	// walk stopped reaching the suites that use it.
	if references == 0 {
		t.Fatalf("no reference to %s anywhere in the module — this gate has stopped watching what it names", retentionScopeBuilder)
	}
}

// TestTheRetentionScopeSinkIsTheOneTheGateMeans keeps the gate above honest about
// what it sanctions.
//
// That gate matches its sink by bare name with the qualifier stripped, because
// resolving a method's receiver from the AST alone is more machinery than the
// check is worth. The cost is that ANY function named EvaluateWorkspace would
// inherit sink status — a two-line shim in a suite, or a second evaluator landing
// in automation or consent, and an unboundable context becomes admissible without
// anyone editing this file. So the name-only match is made true by derivation
// instead of by luck: exactly one declaration, in the package that owns the
// retention engine.
func TestTheRetentionScopeSinkIsTheOneTheGateMeans(t *testing.T) {
	fset := token.NewFileSet()
	var found []string
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
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
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && fn.Name != nil && fn.Name.Name == retentionScopeSink {
				found = append(found, fset.Position(fn.Pos()).String())
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking the module: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("%d declarations of %s (%s) — the scope gate matches its sink by name alone, so a second one silently becomes a sanctioned destination for a context that cannot be denied",
			len(found), retentionScopeSink, strings.Join(found, ", "))
	}
	if !strings.Contains(filepath.ToSlash(found[0]), retentionScopeSinkOwner) {
		t.Errorf("%s is declared at %s, outside %s — the scope gate sanctions it as the retention engine's entry point, so it has to be the retention engine's",
			retentionScopeSink, found[0], retentionScopeSinkOwner)
	}
}

// namesRetentionScopeBuilder reports whether n IS a reference to the builder —
// `integration.RetentionPassCtx` or, from inside that package, a bare
// `RetentionPassCtx`. Applied or not: a function value binds the same principal
// as a call does.
func namesRetentionScopeBuilder(n ast.Node) bool {
	switch node := n.(type) {
	case *ast.SelectorExpr:
		return node.Sel != nil && node.Sel.Name == retentionScopeBuilder
	case *ast.Ident:
		return node.Name == retentionScopeBuilder
	}
	return false
}

// calleeName is the called function's own name, ignoring any qualifier. Sound
// only because TestTheRetentionScopeSinkIsTheOneTheGateMeans pins the sink to a
// single declaration.
func calleeName(call *ast.CallExpr) string {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return fun.Name
	case *ast.SelectorExpr:
		if fun.Sel == nil {
			return ""
		}
		return fun.Sel.Name
	}
	return ""
}
