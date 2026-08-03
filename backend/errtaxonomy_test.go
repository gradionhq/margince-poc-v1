// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// Every error sentinel must have a verdict, on every surface.
//
// The surfaces get there structurally: httperr.Classify is the one decision
// tree, httperr.Write renders it as RFC 7807, and the MCP dispatcher renders
// it as prose an agent can act on. Neither renderer keeps its own list, so
// neither can disagree with the other about whose fault an error is.
//
// That leaves exactly ONE human-maintained list in the chain — httperr's
// sentinel mapping table — and this gate holds it complete: a sentinel added
// to apperrors without a mapping entry classifies as nothing, which means a
// 500 on REST and "the tool failed for an internal reason; retry" on MCP. Both
// are lies about a refusal the system made deliberately, and the MCP one also
// tells the agent to keep re-issuing a call that can never succeed. Adding the
// sentinel and adding its verdict are therefore one change, and this test is
// what makes them one change.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const (
	apperrorsDir  = "internal/shared/apperrors"
	httperrSource = "internal/platform/httperr/httperr.go"
	// sentinelMappingVar is the table httperr consults to turn a sentinel into
	// a status and a machine code.
	sentinelMappingVar = "mapping"
)

// declaredSentinels collects the exported Err* vars the apperrors registry
// declares — the whole sentinel vocabulary, read from the package rather than
// restated here, so a new one is picked up by existing.
func declaredSentinels(t *testing.T) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(apperrorsDir)
	if err != nil {
		t.Fatalf("reading %s: %v", apperrorsDir, err)
	}
	found := map[string]bool{}
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(apperrorsDir, name), nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.VAR {
				continue
			}
			for _, spec := range gen.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, ident := range value.Names {
					if ident.IsExported() && strings.HasPrefix(ident.Name, "Err") {
						found[ident.Name] = true
					}
				}
			}
		}
	}
	if len(found) == 0 {
		t.Fatalf("no sentinels found in %s — the gate is reading the wrong tree", apperrorsDir)
	}
	return found
}

// mappedSentinels collects the sentinels httperr's mapping table names, by
// reading the table itself: the gate must fail when the table falls behind the
// registry, so it cannot be satisfied by a copy of the table kept here.
func mappedSentinels(t *testing.T) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, httperrSource, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", httperrSource, err)
	}
	found := map[string]bool{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok || len(value.Names) != 1 || value.Names[0].Name != sentinelMappingVar {
				continue
			}
			ast.Inspect(spec, func(n ast.Node) bool {
				sel, ok := n.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "apperrors" {
					found[sel.Sel.Name] = true
				}
				return true
			})
		}
	}
	if len(found) == 0 {
		t.Fatalf("no sentinels read out of %s's %s table — the gate is reading the wrong declaration",
			httperrSource, sentinelMappingVar)
	}
	return found
}

func TestEverySentinelHasAWireVerdict(t *testing.T) {
	declared := declaredSentinels(t)
	mapped := mappedSentinels(t)

	var unmapped []string
	for name := range declared {
		if !mapped[name] {
			unmapped = append(unmapped, name)
		}
	}
	sort.Strings(unmapped)
	for _, name := range unmapped {
		t.Errorf("apperrors.%s has no entry in %s's %s table: it would answer 500 on REST and "+
			"\"internal reason, retry\" on MCP. Add its status and machine code there, and to interfaces.md §0.",
			name, httperrSource, sentinelMappingVar)
	}

	var unknown []string
	for name := range mapped {
		if !declared[name] {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(unknown)
	for _, name := range unknown {
		t.Errorf("%s's %s table maps apperrors.%s, which the registry no longer declares — stale entry, remove it",
			httperrSource, sentinelMappingVar, name)
	}
}
