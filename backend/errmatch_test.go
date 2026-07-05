// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// Postgres failures are classified by SQLSTATE/constraint name (the
// storekit.UniqueViolation / CheckViolation helpers), never by message
// text: an error-string substring match silently breaks on a locale
// change, a driver upgrade, or an unrelated error that happens to
// mention the same identifier — and it misclassifies infrastructure
// faults as client faults. This gate fails any hand-written non-test
// file under internal/ that string-matches an error's Error() text.
// Waivers are explicit, keyed by file:function, and each carries a
// self-contained rationale; an unused waiver is itself a failure.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// errTextMatchWaivers are the ratified error-text matches. The only
// admissible ground is a protocol whose machine-readable error code IS
// the message prefix, with no typed accessor in the client library.
var errTextMatchWaivers = map[string]string{
	"internal/platform/events/subscriber.go:isBusyGroup": "RESP wire errors carry their machine code as the message's first token (BUSYGROUP); go-redis exposes no typed accessor, so the prefix match IS the code match",
}

// errorTextCall reports whether expr is a niladic `<recv>.Error()` call —
// the error-message extraction every string match here keys on.
func errorTextCall(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok || len(call.Args) != 0 {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "Error"
}

// matchesErrorText reports whether the node string-matches error text:
// a strings.Contains/HasPrefix/HasSuffix/EqualFold call fed err.Error(),
// or an ==/!= comparison against it.
func matchesErrorText(n ast.Node) (verb string, found bool) {
	switch node := n.(type) {
	case *ast.CallExpr:
		sel, ok := node.Fun.(*ast.SelectorExpr)
		if !ok {
			return "", false
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "strings" {
			return "", false
		}
		switch sel.Sel.Name {
		case "Contains", "HasPrefix", "HasSuffix", "EqualFold":
			for _, arg := range node.Args {
				if errorTextCall(arg) {
					return "strings." + sel.Sel.Name, true
				}
			}
		}
	case *ast.BinaryExpr:
		if node.Op == token.EQL || node.Op == token.NEQ {
			if errorTextCall(node.X) || errorTextCall(node.Y) {
				return "comparison", true
			}
		}
	}
	return "", false
}

func TestNoErrorMessageStringMatching(t *testing.T) {
	for key, rationale := range errTextMatchWaivers {
		if strings.TrimSpace(rationale) == "" {
			t.Errorf("errTextMatchWaivers[%s] has no rationale — a waiver must say why text is the only handle", key)
		}
	}
	used := map[string]bool{}
	fset := token.NewFileSet()
	err := filepath.WalkDir("internal", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		path = filepath.ToSlash(path)
		if !strings.HasSuffix(path, ".go") ||
			strings.HasSuffix(path, "_test.go") ||
			strings.HasSuffix(path, "_gen.go") ||
			strings.HasPrefix(path, "internal/contracts/") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			key := path + ":" + fn.Name.Name
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				verb, found := matchesErrorText(n)
				if !found {
					return true
				}
				if _, waived := errTextMatchWaivers[key]; waived {
					used[key] = true
					return true
				}
				t.Errorf("%s: %s over err.Error() — Postgres failures are classified by SQLSTATE/constraint name (storekit helpers), never by message text",
					fset.Position(n.Pos()), verb)
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for key := range errTextMatchWaivers {
		if !used[key] {
			t.Errorf("errTextMatchWaivers[%s] matches no error-text comparison — stale waiver, remove it", key)
		}
	}
}
