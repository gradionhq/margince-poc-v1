// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// A positional row mapping may only target a struct its own package declares.
//
// `pgx.RowToStructByPos[T]` binds a SELECT's column ORDER to T's field order, so
// the query and the struct have to be edited together. That is fine — and is why
// the pattern is used here — as long as one package owns both. It stops being
// fine the moment T belongs to somebody else: privacy read the custom-field
// catalogue as RowToStructByPos[fieldcatalog.Column] with a two-column SELECT,
// and the day that PORT gained a third field for a different module's read,
// privacy's erasure and its Art. 15 export both began failing on a mismatch
// neither file mentions. Nothing in either module changed.
//
// Only the integration lane could see it, because the mismatch is a pgx runtime
// error rather than a compile error, which is what makes it worth a gate: the
// unit suites, the type checker and the linters were all green.
//
// Derived rather than listed: the rule is read off every call in the tree, so a
// new one is enrolled by existing (review-loop rule 2). The fix at any site is to
// scan into named fields, which also documents which columns that query needs.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// The Go trees this walks — every hand-written module, like the license gate's.
var positionalScanTrees = []string{".", "../extensions", "../fixtures"}

func TestAPositionalRowScanTargetsItsOwnPackagesStruct(t *testing.T) {
	calls := 0
	for _, root := range positionalScanTrees {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				// path != root, because the root itself is "." and a dotted name is
				// exactly what the skip list rejects: skipping it would take the whole
				// tree with it, and the walk would report no findings for the most
				// reassuring possible reason.
				if path != root && skipPositionalScanDir(d.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_gen.go") {
				return nil
			}
			foreign, found := foreignPositionalScans(t, path)
			calls += found
			for _, target := range foreign {
				t.Errorf("%s maps rows positionally into %s, a struct another package declares: the SELECT's column order is then bound to a shape this package does not own, and a field added there breaks this query with a count mismatch that names neither. Scan into named fields instead.",
					path, target)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walking %s: %v", root, err)
		}
	}
	if calls == 0 {
		t.Fatal("no positional row mapping found anywhere — this test has stopped reading the code it derives from")
	}
}

// foreignPositionalScans answers the qualified type arguments of every
// RowToStructByPos in one file, plus how many such calls it holds at all — the
// count is what tells a silent pass from a walk that read nothing.
func foreignPositionalScans(t *testing.T, path string) ([]string, int) {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	var foreign []string
	found := 0
	ast.Inspect(file, func(n ast.Node) bool {
		index, ok := n.(*ast.IndexExpr)
		if !ok || !isPositionalRowMapper(index.X) {
			return true
		}
		found++
		// A qualified type argument (`pkg.T`) is the whole finding: an unqualified
		// one names a type this file's own package declares, which is the sanctioned
		// use. A pointer or slice around it is unwrapped so `[]pkg.T` reads the same.
		if qualified, ok := unwrapPositionalScanType(index.Index).(*ast.SelectorExpr); ok {
			foreign = append(foreign, positionalScanTargetName(qualified))
		}
		return true
	})
	return foreign, found
}

func isPositionalRowMapper(fn ast.Expr) bool {
	selector, ok := fn.(*ast.SelectorExpr)
	return ok && selector.Sel.Name == "RowToStructByPos"
}

func unwrapPositionalScanType(expr ast.Expr) ast.Expr {
	for {
		switch inner := expr.(type) {
		case *ast.StarExpr:
			expr = inner.X
		case *ast.ArrayType:
			expr = inner.Elt
		default:
			return expr
		}
	}
}

func positionalScanTargetName(selector *ast.SelectorExpr) string {
	if pkg, ok := selector.X.(*ast.Ident); ok {
		return pkg.Name + "." + selector.Sel.Name
	}
	return selector.Sel.Name
}

func skipPositionalScanDir(name string) bool {
	return name == "node_modules" || name == "build" || name == "testdata" ||
		strings.HasPrefix(name, ".")
}
