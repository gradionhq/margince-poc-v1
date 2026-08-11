// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/gradionhq/margince/backend/internal/modules/agents/runner"
)

// A catalog entry's allowlist is a list of NAMES, and a name is only as good as
// the registry it resolves against. Two ways it goes wrong, and neither is
// visible at the call site:
//
//   - a misspelt verb silently drops the one tool a goal depends on. The run
//     still starts, reads what it can, and reports a thin answer.
//   - an EMPTY set reads as "no narrowing" at the Job seam (see Job.Tools), so a
//     spec that loses its list quietly regains the whole catalog — the opposite
//     of what the entry is for.
//
// Derived from the live registry rather than from a list kept beside it, so a
// tool that is renamed or retired fails here instead of at 02:00 in a sweep.
func TestEveryAgentSpecNamesRegisteredTools(t *testing.T) {
	registered := map[string]bool{}
	for _, spec := range NewRegistry(nil, SendPath{}).Specs() {
		registered[spec.Name] = true
	}
	for _, spec := range runner.Catalog() {
		if len(spec.Tools) == 0 {
			t.Errorf("agent %q names no tools — an empty allowlist is read as NO narrowing, "+
				"which hands this goal every verb its passport admits", spec.Name)
			continue
		}
		seen := map[string]bool{}
		for _, name := range spec.Tools {
			switch {
			case !registered[name]:
				t.Errorf("agent %q names tool %q, which no registered tool answers to — "+
					"the agent silently loses it, and its goal with it", spec.Name, name)
			case seen[name]:
				t.Errorf("agent %q names tool %q twice; the allowlist is a set", spec.Name, name)
			}
			seen[name] = true
		}
	}
}

// The allowlist only binds a run if the job CARRIES it, and nothing in the type
// system says it must: Job.Tools is an ordinary field whose zero value means "no
// narrowing", so a call site that forgets it produces a working run with the
// whole passport surface — the exact failure the entry exists to prevent, and
// invisible in review because the diff looks complete.
//
// So the obligation is derived from the source: every runner.Job built in this
// package sets Tools. It is a source read for the same reason the migration
// tenant-scope gate is one — the property belongs to the construction site, and
// there is no runtime seam to observe it through that would not mean adding an
// interface with one implementation.
func TestEveryRunnerJobBuiltHereCarriesAnAllowlist(t *testing.T) {
	const file = "runnerservice.go"
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, file, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}

	found := 0
	ast.Inspect(parsed, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok || !isRunnerJob(lit.Type) {
			return true
		}
		found++
		for _, elt := range lit.Elts {
			if kv, ok := elt.(*ast.KeyValueExpr); ok {
				if ident, ok := kv.Key.(*ast.Ident); ok && ident.Name == "Tools" {
					return true
				}
			}
		}
		t.Errorf("%s: the runner.Job built at %s sets no Tools — the run is then narrowed by the "+
			"passport alone, and the agent's catalog entry binds nothing",
			file, fset.Position(lit.Pos()))
		return true
	})
	if found == 0 {
		t.Fatalf("%s builds no runner.Job — this gate is reading the wrong file, "+
			"which is worse than not having it", file)
	}
}

// isRunnerJob reports whether a composite literal's type is runner.Job.
func isRunnerJob(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "runner" && sel.Sel.Name == "Job"
}

// The two shipped agents are the reason the allowlist exists, so the property
// that motivated it is asserted rather than left to the reader: what each goal
// needs is a strict subset of what its scopes admit, and the gap is the verbs
// nothing but the entry can withhold.
func TestTheShippedAgentsAreNarrowerThanTheirScopesAllow(t *testing.T) {
	specs := NewRegistry(nil, SendPath{}).Specs()
	byName := map[string]string{}
	for _, spec := range specs {
		byName[spec.Name] = string(spec.RequiredScope)
	}
	for _, spec := range runner.Catalog() {
		needed := map[string]bool{}
		for _, name := range spec.Tools {
			needed[byName[name]] = true
		}
		var admitted, withheld []string
		for _, tool := range specs {
			if !needed[string(tool.RequiredScope)] {
				continue
			}
			admitted = append(admitted, tool.Name)
			if !containsName(spec.Tools, tool.Name) {
				withheld = append(withheld, tool.Name)
			}
		}
		if len(withheld) == 0 {
			t.Errorf("agent %q withholds nothing its scopes admit (%d tools) — either the entry is "+
				"redundant or it has drifted into naming everything", spec.Name, len(admitted))
			continue
		}
		t.Logf("agent %-24s names %d of the %d tools its scopes admit; withholds %s",
			spec.Name, len(spec.Tools), len(admitted), strings.Join(withheld, ", "))
	}
}

func containsName(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}
