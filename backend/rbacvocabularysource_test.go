// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// The policy.go vocabulary parser, shared by two gates with different lanes:
// rbacvocabulary_test.go (both lanes — it only reads the working tree) and the
// legacy-cohort gates (unit lane only — they read git history). It lives here,
// untagged, so the tagged half can be excluded without taking this with it.
//
// It deliberately does NOT read identity/internal/policy as a package: that
// package is import-fenced to internal/modules/identity/**, so this parses the
// declaration, in the manner of enumsync_test.go.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
)

const policyFile = "internal/modules/identity/internal/policy/policy.go"

// coreObjectsFromSource extracts the string values of policy.go's coreObjects
// declaration as it stands today. Derived, never restated — a renamed or moved
// declaration fails loudly rather than silently shrinking a gate's coverage.
//
// It deliberately does NOT read identity/internal/policy as a package: that
// package is import-fenced to internal/modules/identity/**, so this parses the
// declaration, in the manner of enumsync_test.go.
func coreObjectsFromSource(t *testing.T) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), policyFile, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", policyFile, err)
	}
	objects := coreObjectsIn(t, file)
	if len(objects) == 0 {
		t.Fatalf("parsed no objects from %s; the declaration this gate derives from has moved", policyFile)
	}
	return objects
}

func coreObjectsIn(t *testing.T, file *ast.File) []string {
	t.Helper()
	var objects []string
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) != 1 || vs.Names[0].Name != "coreObjects" {
				continue
			}
			objects = append(objects, stringLiterals(t, vs.Values)...)
		}
	}
	return objects
}

func stringLiterals(t *testing.T, values []ast.Expr) []string {
	t.Helper()
	var out []string
	for _, value := range values {
		lit, ok := value.(*ast.CompositeLit)
		if !ok {
			continue
		}
		for _, element := range lit.Elts {
			basic, ok := element.(*ast.BasicLit)
			if !ok || basic.Kind != token.STRING {
				continue
			}
			unquoted, err := strconv.Unquote(basic.Value)
			if err != nil {
				t.Fatalf("unquoting %s: %v", basic.Value, err)
			}
			out = append(out, unquoted)
		}
	}
	return out
}
