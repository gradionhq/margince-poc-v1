// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGenerateWritesHandlerAndTestWithSPDXHeader is the AC-W1 mechanical
// property: two files land on disk, both carrying the license header
// license_test.go requires of every hand-written (non-*_gen.go) file —
// a scaffold that skips it fails `make check` by construction.
func TestGenerateWritesHandlerAndTestWithSPDXHeader(t *testing.T) {
	dir := t.TempDir()

	handlerPath, testPath, err := generate(dir, "my_test_handler")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	wantHandler := filepath.Join(dir, "workflows_my_test_handler.go")
	wantTest := filepath.Join(dir, "workflows_my_test_handler_test.go")
	if handlerPath != wantHandler {
		t.Errorf("handlerPath = %q, want %q", handlerPath, wantHandler)
	}
	if testPath != wantTest {
		t.Errorf("testPath = %q, want %q", testPath, wantTest)
	}

	const spdx = "// SPDX-License-Identifier: BUSL-1.1\n// SPDX-FileCopyrightText: 2026 Gradion\n"
	for _, p := range []string{handlerPath, testPath} {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("reading %s: %v", p, err)
		}
		if !strings.HasPrefix(string(b), spdx) {
			t.Errorf("%s does not start with the BUSL SPDX header:\n%s", p, b)
		}
	}
}

// TestGenerateEmittedHandlerParsesAndDeclaresHandlerMethods proves the
// scaffold is structurally valid Go that would satisfy
// internal/shared/ports/workflow.Handler once compiled inside the
// automation package: it parses, and declares every method the
// interface requires (Spec, Match, Plan, Apply, IdempotencyKey) plus the
// struct they hang off.
func TestGenerateEmittedHandlerParsesAndDeclaresHandlerMethods(t *testing.T) {
	dir := t.TempDir()
	handlerPath, _, err := generate(dir, "flag_idle_deals")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, handlerPath, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("emitted handler is not valid Go: %v", err)
	}
	if file.Name.Name != "automation" {
		t.Errorf("package name = %q, want automation", file.Name.Name)
	}

	methods := map[string]bool{}
	var structDecl bool
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Recv != nil && len(d.Recv.List) == 1 {
				methods[d.Name.Name] = true
			}
		case *ast.GenDecl:
			if d.Tok != token.TYPE {
				continue
			}
			for _, spec := range d.Specs {
				if ts, ok := spec.(*ast.TypeSpec); ok {
					if _, ok := ts.Type.(*ast.StructType); ok {
						structDecl = true
					}
				}
			}
		}
	}
	if !structDecl {
		t.Error("emitted handler declares no struct type")
	}
	for _, want := range []string{"Spec", "Match", "Plan", "Apply", "IdempotencyKey"} {
		if !methods[want] {
			t.Errorf("emitted handler declares no %s method (workflow.Handler requires it)", want)
		}
	}
}

// TestGenerateEmittedTestReferencesHandlerSymbol checks the generated
// test stub actually exercises the handler it was scaffolded for, not
// some other type — a copy-pasted stub that silently tests nothing would
// still "pass".
func TestGenerateEmittedTestReferencesHandlerSymbol(t *testing.T) {
	dir := t.TempDir()
	_, testPath, err := generate(dir, "flag_idle_deals")
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	b, err := os.ReadFile(testPath)
	if err != nil {
		t.Fatalf("reading %s: %v", testPath, err)
	}
	if !strings.Contains(string(b), "flagIdleDeals") {
		t.Errorf("emitted test stub never references the flagIdleDeals handler symbol:\n%s", b)
	}

	fset := token.NewFileSet()
	if _, err := parser.ParseFile(fset, testPath, nil, parser.SkipObjectResolution); err != nil {
		t.Fatalf("emitted test stub is not valid Go: %v", err)
	}
}

// TestGenerateRefusesToOverwrite is the load-bearing write-once property
// (§3.6): a second run for the same name must refuse rather than clobber
// a handler the developer has since filled in.
func TestGenerateRefusesToOverwrite(t *testing.T) {
	dir := t.TempDir()

	handlerPath, testPath, err := generate(dir, "route_new_widget")
	if err != nil {
		t.Fatalf("first generate: %v", err)
	}
	before, err := os.ReadFile(handlerPath)
	if err != nil {
		t.Fatalf("reading %s: %v", handlerPath, err)
	}

	// Simulate the developer having filled in the scaffold.
	edited := append(before, []byte("\n// developer edit, must survive\n")...)
	if err := os.WriteFile(handlerPath, edited, 0o600); err != nil {
		t.Fatalf("seeding developer edit: %v", err)
	}

	if _, _, err := generate(dir, "route_new_widget"); err == nil {
		t.Fatal("second generate for the same name succeeded — write-once must refuse")
	}

	after, err := os.ReadFile(handlerPath)
	if err != nil {
		t.Fatalf("reading %s after refused overwrite: %v", handlerPath, err)
	}
	if string(after) != string(edited) {
		t.Error("the refused overwrite still modified the existing handler file")
	}
	// The test-stub sibling must be equally protected even on a fresh
	// second run where only the handler file was touched.
	if _, err := os.Stat(testPath); err != nil {
		t.Fatalf("test stub missing after refused overwrite: %v", err)
	}
}

// TestGenerateRejectsInvalidName guards the snake_case contract the
// catalog key and Spec().Name share with every other starter handler.
func TestGenerateRejectsInvalidName(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"", "CamelCase", "has space", "trailing_", "-leading-dash"} {
		if _, _, err := generate(dir, name); err == nil {
			t.Errorf("generate(%q) succeeded, want a snake_case validation error", name)
		}
	}
}
