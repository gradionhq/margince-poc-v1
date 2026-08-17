// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// OPS-CFG-2: configuration is read once at the composition root and passed
// down; nothing reads the process environment on its own behalf.
//
// The rule had nothing holding it, and the drift is what it cost: 61 MARGINCE_*
// variables live in Go against 34 written down, with six production variables —
// MARGINCE_SCHEMA_DSN among them, which is required to create custom fields —
// documented nowhere at all. A variable no one can enumerate cannot be
// validated, templated or generated from, so the gap grows silently.
//
// Derived from the tree rather than from an allowlist (review rule 2), so a new
// package with a new environment read fails here without anyone remembering to
// register it.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// envReadingCall names the two ways a Go program reads its environment by name.
// os.Environ and os.ExpandEnv are deliberately absent: neither names a variable,
// so neither can contribute to a surface this rule is about enumerating.
var envReadingCall = map[string]bool{"Getenv": true, "LookupEnv": true}

// theSeam is the one package permitted to make that call.
const theSeam = "internal/platform/config"

// TestOnlyTheConfigSeamReadsTheEnvironment walks every hand-written Go file and
// fails on an os.Getenv / os.LookupEnv outside the seam.
//
// Test files and test-support packages are exempt, and the exemption is narrow
// on purpose: a harness that reads MARGINCE_TEST_DSN is telling a suite where
// its database is, which is not installation configuration and appears in no
// deployment's template. The check for that is the variable's own name, so a
// helper cannot quietly read a PRODUCT variable under a test-support banner.
func TestOnlyTheConfigSeamReadsTheEnvironment(t *testing.T) {
	fset := token.NewFileSet()
	for _, root := range []string{".", "../extensions", "../fixtures"} {
		walkForEnvReads(t, fset, root)
	}
}

// walkForEnvReads checks one hand-written Go tree. The three are the same trees
// the license header gate holds, for the same reason: each extension unit is
// its own module and ships the same product.
func walkForEnvReads(t *testing.T, fset *token.FileSet, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if skippedTreeForConfigSeam(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		slash := filepath.ToSlash(path)
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") ||
			strings.HasSuffix(path, "_gen.go") || strings.Contains(slash, theSeam) {
			return nil
		}
		file, parseErr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if parseErr != nil {
			return parseErr
		}
		for _, call := range envReadsIn(file) {
			reportEnvRead(t, slash, fset.Position(call.Pos()).Line, call)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
}

// envReadsIn collects every os.Getenv / os.LookupEnv call expression in a file.
func envReadsIn(file *ast.File) []*ast.CallExpr {
	var found []*ast.CallExpr
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if ok && pkg.Name == "os" && envReadingCall[sel.Sel.Name] {
			found = append(found, call)
		}
		return true
	})
	return found
}

// reportEnvRead fails unless the read is a test-support one, judged by the
// variable's own name rather than by where the file sits.
func reportEnvRead(t *testing.T, rel string, line int, call *ast.CallExpr) {
	t.Helper()
	if name, ok := literalArg(call); ok && isTestOnlyVariable(name) {
		return
	}
	t.Errorf("%s:%d reads the environment directly.\n"+
		"Configuration is read once at the composition root and passed down (OPS-CFG-2): take a "+
		"config.Lookup and let cmd/<role> supply config.FromOS.\n"+
		"A read here is invisible to the generated template and the schema, which is how six "+
		"production variables came to be documented nowhere.", rel, line)
}

// literalArg returns the variable name when the call names one literally. A
// computed name (a map lookup, a const) cannot be judged here, so it is held to
// the rule rather than waved through.
func literalArg(call *ast.CallExpr) (string, bool) {
	if len(call.Args) != 1 {
		return "", false
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	return strings.Trim(lit.Value, `"`), true
}

// envPrefix is spelled separately from the two suffixes below because the
// sibling documentation gate (envcontract_test.go) reads every quoted
// MARGINCE_* literal in the tree as a variable somebody must document. A bare
// prefix is not a variable, and writing one whole here would demand a row in
// configuration.md for a name no process ever reads.
const envPrefix = "MARGINCE_"

// isTestOnlyVariable reports whether a name belongs to the suite's own
// plumbing — where the test database lives, whether to record a benchmark —
// rather than to anything an installation configures.
func isTestOnlyVariable(name string) bool {
	return strings.HasPrefix(name, envPrefix+"TEST_") || strings.HasPrefix(name, envPrefix+"BENCH_")
}

// skippedTreeForConfigSeam names directories that hold no hand-written product
// Go: dependencies, build output, and sibling worktrees a parallel session may
// have left in the tree.
func skippedTreeForConfigSeam(name string) bool {
	switch name {
	case "node_modules", "vendor", "build", "dist", ".git", ".claude", "testdata":
		return true
	// backend/tools is a separate Go module of developer commands — codegen and
	// the demo seeder — that no product binary imports. Its variables configure
	// a one-off invocation, not an installation, so they belong to no
	// deployment's template and are not what OPS-CFG-2 is about.
	case "tools":
		return true
	default:
		return false
	}
}
