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

func TestEveryJobArgsDeclaresExactlyOneRole(t *testing.T) {
	byType := methodsByType(t, filepath.Join("internal", "compose"))
	jobs := 0
	for typeName, methods := range byType {
		if !methods["Kind"] {
			continue
		}
		jobs++
		scoped, fleet := methods["WorkspaceID"], methods["FleetWide"]
		switch {
		case scoped && fleet:
			t.Errorf("%s declares both WorkspaceID() and FleetWide(): a job does one workspace's work or dispatches, never both", typeName)
		case !scoped && !fleet:
			t.Errorf("%s is a River job (it declares Kind()) but no role. Add WorkspaceID() ids.UUID if its work belongs to one workspace, or FleetWide() if it only enumerates and enqueues.", typeName)
		}
	}
	if jobs < jobArgsFloor {
		t.Fatalf("found only %d job args types, expected at least %d — the walker matched nothing and this gate would pass vacuously", jobs, jobArgsFloor)
	}
}
