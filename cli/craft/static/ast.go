// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package static

import (
	"go/ast"
	"strings"
)

// isSelectorCall reports whether n is a call of the form pkg.method(...).
func isSelectorCall(n ast.Node, pkg, method string) bool {
	call, ok := n.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != method {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == pkg
}

// testParamName returns the name of a Test function's *testing.T parameter, or
// "" if the signature isn't `func(t *testing.T)` with a named receiver we can
// track (an unnamed param is unjudgeable, so we skip it rather than guess).
func testParamName(fn *ast.FuncDecl) string {
	if fn.Type.Params == nil || len(fn.Type.Params.List) == 0 {
		return ""
	}
	p := fn.Type.Params.List[0]
	star, ok := p.Type.(*ast.StarExpr)
	if !ok {
		return ""
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "T" {
		return ""
	}
	if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "testing" {
		return ""
	}
	if len(p.Names) == 0 {
		return ""
	}
	return p.Names[0].Name
}

// assertMethods are the *testing.T calls (and subtest/skip forms) that make a
// test able to fail or defer its judgement to a subtest.
var assertMethods = map[string]bool{
	"Error": true, "Errorf": true, "Fatal": true, "Fatalf": true,
	"Fail": true, "FailNow": true, "Skip": true, "Skipf": true, "Run": true,
}

// assertPackages are the common assertion helper packages; a call on any of
// them counts as an assertion.
var assertPackages = map[string]bool{
	"assert": true, "require": true, "is": true, "should": true, "must": true, "expect": true,
}

// hasAssertion reports whether body can fail a test: a t.Error/Fatal/…, a
// subtest, a call to a known assertion package, or any helper call that
// receives t (delegated assertion).
func hasAssertion(body *ast.BlockStmt, t string) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			if x, ok := sel.X.(*ast.Ident); ok {
				if x.Name == t && assertMethods[sel.Sel.Name] {
					found = true
				}
				if assertPackages[x.Name] {
					found = true
				}
			}
		}
		for _, arg := range call.Args {
			if id, ok := arg.(*ast.Ident); ok && id.Name == t {
				found = true // a helper that takes t asserts on our behalf
			}
		}
		return !found
	})
	return found
}

// fieldsOf flattens the fields of the given field lists, skipping nils.
func fieldsOf(lists ...*ast.FieldList) []*ast.Field {
	var out []*ast.Field
	for _, l := range lists {
		if l != nil {
			out = append(out, l.List...)
		}
	}
	return out
}

// isBareAny reports whether expr is `any` or an empty `interface{}`.
func isBareAny(expr ast.Expr) bool {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name == "any"
	case *ast.InterfaceType:
		return t.Methods == nil || len(t.Methods.List) == 0
	}
	return false
}

// panicAllowed reports whether a function may panic by convention: the
// Must-constructor idiom, package init, and main.
func panicAllowed(name string) bool {
	return name == "init" || name == "main" || strings.HasPrefix(name, "Must")
}
