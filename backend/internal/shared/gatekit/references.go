// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package gatekit

import (
	"go/ast"
	"path"
	"strconv"
)

// References reports whether a parsed file reaches symbol in the package at
// importPath — as a call, or as a value it could call later.
//
// It is how a path-scoped gate names the site it judges without type-aware
// analysis of the whole module, and it exists once because every gate that
// wants it wants the same four things:
//
//   - NAMING is enough. A check that matched only `pkg.Sym(...)` is walked past
//     by `f := pkg.Sym; f(...)`, and following a function value needs the type
//     information a fitness test does not have. A file has no reason to hold a
//     symbol it never intends to use, so a gate that treats a reference as use
//     closes that route; a file that genuinely needs the symbol for something
//     else says so through a waiver, which is visible, rather than through a
//     spelling the gate cannot see.
//   - The QUALIFIER is the caller's choice. `import dm ".../dbmigrate"` and a
//     dot-import that leaves the symbol bare reach it exactly as much as the
//     canonical spelling does, so the name is resolved through the file's own
//     imports rather than assumed.
//   - A file INSIDE the package reaches the symbol with no import at all.
//   - PROSE IS NOT A REFERENCE. A gate's own doc comment and failure message
//     name the symbol it hunts, and a text scan over a tree that includes the
//     gate flags the gate. Comments and string literals carry no identifier for
//     the syntax tree to match, so this cannot.
//
// The in-package case keys on the package clause matching importPath's last
// element, which is this repo's convention for every package a gate targets. A
// package whose name differs from its directory would need its own spelling.
func References(file *ast.File, importPath, symbol string) bool {
	qualifier, dotImported := importedAs(file, importPath)
	inPackage := file.Name != nil && file.Name.Name == path.Base(importPath)
	if qualifier == "" && !dotImported && !inPackage {
		return false
	}
	bare := dotImported || inPackage
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		if found {
			return false
		}
		found = reachesSymbol(n, qualifier, symbol, bare)
		return !found
	})
	return found
}

// reachesSymbol answers the question for ONE node: is this the symbol, under
// whichever of the three spellings this file can reach it by.
func reachesSymbol(n ast.Node, qualifier, symbol string, bare bool) bool {
	switch node := n.(type) {
	case *ast.SelectorExpr:
		// qualifier.Symbol, whether or not it is being applied here.
		if qualifier == "" || node.Sel == nil || node.Sel.Name != symbol {
			return false
		}
		pkg, ok := node.X.(*ast.Ident)
		return ok && pkg.Name == qualifier
	case *ast.Ident:
		// A bare symbol: reachable under a dot-import, or from inside the
		// package that declares it.
		return bare && node.Name == symbol
	}
	return false
}

// importedAs returns the identifier this file binds importPath to, and whether
// it was dot-imported. Both are empty/false when the file does not import it at
// all, which is the cheap way to skip most of a tree.
func importedAs(file *ast.File, importPath string) (qualifier string, dotImported bool) {
	for _, spec := range file.Imports {
		imported, err := strconv.Unquote(spec.Path.Value)
		if err != nil || imported != importPath {
			continue
		}
		switch {
		case spec.Name == nil:
			return path.Base(importPath), false
		case spec.Name.Name == ".":
			return "", true
		case spec.Name.Name == "_":
			// Imported for side effects only; it cannot be called through.
			return "", false
		default:
			return spec.Name.Name, false
		}
	}
	return "", false
}
