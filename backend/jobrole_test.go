// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// Every River job declares its role, and the declaration is the contract:
// a job either does tenant work for ONE workspace (jobs.WorkspaceScoped,
// method WorkspaceID) or only scans and enqueues (jobs.FleetWide). A job
// that declares neither is the shape this gate exists to prevent — an
// inline `for each workspace` loop inside one job row, whose per-workspace
// failures have nowhere durable to land, so River records success while
// tenants silently failed.
//
// api/jobs.yaml states that role per kind and jobkinds_gen.go turns each
// statement into a compile-time assertion, so the role of a DECLARED kind is
// the compiler's to check. What is left here is the two halves those generated
// assertions provably cannot reach — a type the contract has never heard of,
// and a type that answers to both roles at once.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// jobArgsFloor guards against a vacuous pass: a walker that silently
// matched nothing would otherwise report green. The tree holds ~30 job
// kinds before the dispatcher split and more after; this floor only has to
// be low enough never to false-alarm.
const jobArgsFloor = 25

// goFilesUnder returns every hand-written .go file beneath root.
//
// Walked RECURSIVELY, not globbed: compose grows subpackages under a
// named-trigger policy (compose/briefs is the pilot), and a job args type
// or a worker in one of them would be invisible to a flat glob — a gate
// with a blind spot that widens every time the tree does.
func goFilesUnder(root string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	return paths, err
}

// parseGoFilesUnder parses every hand-written Go file beneath dir.
func parseGoFilesUnder(t *testing.T, dir string) (*token.FileSet, []*ast.File) {
	t.Helper()
	paths, err := goFilesUnder(dir)
	if err != nil {
		t.Fatalf("walking %s: %v", dir, err)
	}
	fset := token.NewFileSet()
	files := make([]*ast.File, 0, len(paths))
	for _, path := range paths {
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}
		files = append(files, file)
	}
	return fset, files
}

// methodsByType returns, per declared type in dir, the set of method names
// on it.
func methodsByType(t *testing.T, dir string) map[string]map[string]bool {
	t.Helper()
	_, files := parseGoFilesUnder(t, dir)
	byType := map[string]map[string]bool{}
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			recv := receiverTypeName(fn)
			if recv == "" {
				continue
			}
			if byType[recv] == nil {
				byType[recv] = map[string]bool{}
			}
			byType[recv][fn.Name.Name] = true
		}
	}
	return byType
}

// collectUnionTerms records the bare type names in one embedded interface
// term. A union parses as a left-leaning `|` chain, so the recursion follows
// the chain; the embedded river.JobArgs is a SelectorExpr, not a kind, and is
// skipped by naming only *ast.Ident.
func collectUnionTerms(e ast.Expr, into map[string]bool) {
	switch term := e.(type) {
	case *ast.Ident:
		into[term.Name] = true
	case *ast.BinaryExpr:
		collectUnionTerms(term.X, into)
		collectUnionTerms(term.Y, into)
	}
}

// declaredKindTypes reads the union terms of declaredJobArgs out of the
// generated file — the same closed set the compiler enforces at every
// registration site. Read from the source rather than imported because this
// package may not depend on internal/platform (.go-arch-lint.yml), and
// because the claim is about the generated set itself.
func declaredKindTypes(t *testing.T) map[string]bool {
	t.Helper()
	path := filepath.Join("internal", "compose", "jobkinds_gen.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	declared := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.TypeSpec)
		if !ok || spec.Name.Name != "declaredJobArgs" {
			return true
		}
		iface, ok := spec.Type.(*ast.InterfaceType)
		if !ok {
			return false
		}
		for _, field := range iface.Methods.List {
			if len(field.Names) > 0 {
				continue // a method, not an embedded term
			}
			collectUnionTerms(field.Type, declared)
		}
		return false
	})
	if len(declared) < jobArgsFloor {
		t.Fatalf("read only %d declared args types out of %s, expected at least %d — the union walk matched almost nothing", len(declared), path, jobArgsFloor)
	}
	return declared
}

// TestEveryJobArgsTypeIsDeclaredInTheContract is the half the generated
// assertions cannot reach. They name only kinds the contract already carries,
// so a type someone added and merely ENQUEUED — Runner.Enqueue takes a bare
// river.JobArgs, and cmd/api holds seven inserter handles — would be invisible
// to them. This walks the tree instead, so the type existing at all is enough.
func TestEveryJobArgsTypeIsDeclaredInTheContract(t *testing.T) {
	byType := methodsByType(t, filepath.Join("internal", "compose"))
	declared := declaredKindTypes(t)

	found := 0
	for typeName, methods := range byType {
		if !methods["Kind"] {
			continue
		}
		found++
		if !declared[typeName] {
			t.Errorf("%s is a River job (it declares Kind()) but is not in api/jobs.yaml. Add it there and run `make gen` — an undeclared kind runs on River's one-minute default and is invisible to both job surfaces.", typeName)
		}
	}
	if found < jobArgsFloor {
		t.Fatalf("found only %d job args types, expected at least %d — the walker matched nothing and this gate would pass vacuously", found, jobArgsFloor)
	}
}

// TestNoJobArgsDeclaresBothRoles holds what the generated assertions cannot:
// Go has no negative constraint, so `var _ jobs.FleetWide = T{}` is satisfied
// just as happily by a type that ALSO implements WorkspaceID. Only a walker
// sees both at once.
func TestNoJobArgsDeclaresBothRoles(t *testing.T) {
	byType := methodsByType(t, filepath.Join("internal", "compose"))
	roled := 0
	for typeName, methods := range byType {
		scoped, fleet := methods["WorkspaceID"], methods["FleetWide"]
		if !scoped && !fleet {
			continue
		}
		roled++
		if scoped && fleet {
			t.Errorf("%s declares both WorkspaceID() and FleetWide(): a job does one workspace's work or dispatches, never both", typeName)
		}
	}
	if roled < jobArgsFloor {
		t.Fatalf("found only %d types declaring a role, expected at least %d — the walker matched almost nothing and this gate would pass vacuously", roled, jobArgsFloor)
	}
}
