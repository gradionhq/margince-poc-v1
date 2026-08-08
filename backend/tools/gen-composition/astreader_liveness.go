// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"fmt"
	"go/ast"
	"go/token"
)

// rejectLiveInitializers refuses package-level init() and any package-level
// var whose initializer contains a call, in a unit's root package.
//
// Both run at IMPORT — before cmd/api/main.go's composition.Extensions()
// builds anything, before the runtime pool exists, and long before
// RegisterExtensions validates the composed set. Task 1's runtime-role
// assertion cannot reach that window because there is no runtime yet to
// assert about; this AST walk is the only gate that can. Without it, a unit
// could dial a connection, spawn a goroutine, or otherwise do live work
// merely by being imported, which defeats "a declaration is inert data"
// before New() is even called.
//
// This walk targets CALLS specifically, not "runs no code" in general: a
// var initializer that merely holds a composite or basic literal (like
// yogi's var quotes = []string{...}) is unaffected because it contains no
// call, and the New() literal-only reader already welcomes exactly that
// shape. containsCall does not claim to enumerate every way an initializer
// could do work at import — a channel receive (var v = <-ch) is a
// *ast.UnaryExpr, not a call, and would slip past this walk the same way a
// composite literal correctly does. That is accepted as out of scope: it
// needs a channel without make to reach, which nothing in this generator's
// literal-only declaration idiom can construct, so no case exercises it. A
// call-bearing initializer is refused even when it is syntactically a type
// conversion: the AST cannot tell a conversion from a call that dials out,
// so — the same reasoning as the Handle rule in astreader.go — the
// conservative rule is the only one that keeps the claim checkable.
func rejectLiveInitializers(pkgs map[string]*ast.Package, fset *token.FileSet) error {
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			for _, decl := range f.Decls {
				switch d := decl.(type) {
				case *ast.FuncDecl:
					if d.Recv == nil && d.Name.Name == "init" {
						return fmt.Errorf("%s: func init is not permitted in a unit's root package — it runs at import, before the declaration is validated", fset.Position(d.Pos()))
					}
				case *ast.GenDecl:
					if d.Tok != token.VAR {
						continue
					}
					for _, spec := range d.Specs {
						vs, ok := spec.(*ast.ValueSpec)
						if !ok {
							continue
						}
						for _, val := range vs.Values {
							if containsCall(val) {
								return fmt.Errorf("%s: a package-level var initializer must not call a function — it runs at import, before the declaration is validated", fset.Position(val.Pos()))
							}
						}
					}
				}
			}
		}
	}
	return nil
}

// containsCall reports whether expr's tree holds any call expression,
// however deeply nested (an argument, a composite literal field, ...).
func containsCall(expr ast.Expr) bool {
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		if _, ok := n.(*ast.CallExpr); ok {
			found = true
			return false
		}
		return true
	})
	return found
}
