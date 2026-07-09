// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package static

import (
	"go/ast"
	"go/token"
	"regexp"
	"strings"
)

// checks is the registry. Order is display order only; every check runs on
// every file. Each row maps to an anti-tell in architecture/15 §2 (or a
// craft-rubric point in §3) that is decidable from the syntax tree alone.
var checks = []struct {
	name string
	run  func(fc *fileContext, cfg Config) []Finding
}{
	{"swallowed-errors", swallowedErrors},  // §F / T2: the top reliability smell
	{"test-sleep", testSleep},              // T11: sleeps make suites flaky
	{"assertion-free-test", assertionFree}, // T11: a test that can't fail is a false green
	{"boolean-trap", booleanTrap},          // §C: unreadable at the call site
	{"naked-any", nakedAny},                // T6: type escape hatch
	{"panic-in-domain", panicInDomain},     // §F: panics across a domain boundary
	{"todo-without-ref", todoWithoutRef},   // T8: an untracked marker is dead intent
	{"large-file", largeFile},              // §3 / architecture/11: the size smell
	{"long-func", longFunc},                // §C: god-function / arrow code
}

func (fc *fileContext) line(pos token.Pos) int { return fc.fset.Position(pos).Line }

// swallowedErrors flags `_ = call()` — blank-assigning a call result silently
// drops whatever it returned, most often an error. errcheck (in golangci)
// treats an explicit `_ =` as a sanctioned ignore, so this is the gap it
// leaves open. BLOCKER: the rule is objective and the fix is cheap.
func swallowedErrors(fc *fileContext, _ Config) []Finding {
	var out []Finding
	ast.Inspect(fc.file, func(n ast.Node) bool {
		a, ok := n.(*ast.AssignStmt)
		if !ok || a.Tok != token.ASSIGN || len(a.Lhs) != 1 || len(a.Rhs) != 1 {
			return true
		}
		if id, ok := a.Lhs[0].(*ast.Ident); !ok || id.Name != "_" {
			return true
		}
		if _, ok := a.Rhs[0].(*ast.CallExpr); !ok {
			return true
		}
		out = append(out, newFinding("swallowed-errors", Blocker, fc.path, fc.line(a.Pos()),
			"blank-assigning a call result discards a possible error — handle it, or waive with //craft:ignore swallowed-errors <reason>"))
		return true
	})
	return out
}

// testSleep flags time.Sleep inside a _test.go file — the classic root cause
// of a flaky suite. Synchronize on the condition, not the wall clock.
func testSleep(fc *fileContext, _ Config) []Finding {
	if !fc.isTest {
		return nil
	}
	var out []Finding
	ast.Inspect(fc.file, func(n ast.Node) bool {
		if isSelectorCall(n, "time", "Sleep") {
			out = append(out, newFinding("test-sleep", Blocker, fc.path, fc.line(n.Pos()),
				"time.Sleep in a test invites flakiness — wait on the condition, not the clock"))
		}
		return true
	})
	return out
}

// assertionFree flags a Test function with no assertion, no pkg-assert call,
// no subtest, and no helper that receives t — such a test can only fail by
// panicking, so it is a false green. MAJOR: helper detection is heuristic.
func assertionFree(fc *fileContext, _ Config) []Finding {
	if !fc.isTest {
		return nil
	}
	var out []Finding
	for _, d := range fc.file.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Body == nil || !strings.HasPrefix(fn.Name.Name, "Test") {
			continue
		}
		t := testParamName(fn)
		if t == "" || hasAssertion(fn.Body, t) {
			continue
		}
		out = append(out, newFinding("assertion-free-test", Major, fc.path, fc.line(fn.Pos()),
			"%s has no assertions — it can only fail by panicking", fn.Name.Name))
	}
	return out
}

// booleanTrap flags a signature carrying two or more bool parameters —
// `f(true, false)` is unreadable at the call site.
func booleanTrap(fc *fileContext, _ Config) []Finding {
	var out []Finding
	for _, d := range fc.file.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Type.Params == nil {
			continue
		}
		bools := 0
		for _, p := range fn.Type.Params.List {
			if id, ok := p.Type.(*ast.Ident); ok && id.Name == "bool" {
				bools += max(len(p.Names), 1)
			}
		}
		if bools >= 2 {
			out = append(out, newFinding("boolean-trap", Major, fc.path, fc.line(fn.Pos()),
				"%d bool parameters are a boolean trap — use named types or an options struct", bools))
		}
	}
	return out
}

// nakedAny flags bare `any` / `interface{}` in a function's parameters or
// results (not its type parameters, where `any` is the right constraint).
func nakedAny(fc *fileContext, _ Config) []Finding {
	var out []Finding
	for _, d := range fc.file.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok {
			continue
		}
		seen := map[int]bool{}
		for _, f := range fieldsOf(fn.Type.Params, fn.Type.Results) {
			if !isBareAny(f.Type) {
				continue
			}
			line := fc.line(f.Pos())
			if seen[line] {
				continue
			}
			seen[line] = true
			out = append(out, newFinding("naked-any", Major, fc.path, line,
				"bare any/interface{} in a signature — use a concrete or constrained type, or waive with //craft:ignore naked-any <reason>"))
		}
	}
	return out
}

// panicInDomain flags panic() in a domain module (outside Must*/init/main and
// outside tests) — a domain error must flow through the sentinel set, not a
// panic that unwinds across the boundary.
func panicInDomain(fc *fileContext, _ Config) []Finding {
	if !fc.inDomain || fc.isTest {
		return nil
	}
	var out []Finding
	for _, d := range fc.file.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Body == nil || panicAllowed(fn.Name.Name) {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "panic" {
					out = append(out, newFinding("panic-in-domain", Major, fc.path, fc.line(call.Pos()),
						"panic() in a domain module — return an error through the sentinel set instead"))
				}
			}
			return true
		})
	}
	return out
}

var (
	todoRE    = regexp.MustCompile(`(?i)\b(TODO|FIXME)\b`)
	todoRefRE = regexp.MustCompile(`#\d|https?://|\(\w+\)|issue`)
)

// todoWithoutRef flags a to-do / fix-me marker that names no owner, issue, or
// URL — an untracked intent that will rot silently.
func todoWithoutRef(fc *fileContext, _ Config) []Finding {
	var out []Finding
	for _, g := range fc.file.Comments {
		for _, c := range g.List {
			if todoRE.MatchString(c.Text) && !todoRefRE.MatchString(c.Text) {
				out = append(out, newFinding("todo-without-ref", Minor, fc.path, fc.line(c.Slash),
					"marker without an issue reference — link an owner, issue, or URL"))
			}
		}
	}
	return out
}

// largeFile flags a file past the architecture/11 §3 size smell threshold.
func largeFile(fc *fileContext, cfg Config) []Finding {
	if fc.lineN <= cfg.MaxFileLines {
		return nil
	}
	return []Finding{newFinding("large-file", Major, fc.path, 1,
		"file is %d lines (> %d) — split it by concept", fc.lineN, cfg.MaxFileLines)}
}

// longFunc flags a function whose body exceeds the line ceiling — the
// god-function / arrow-code smell.
func longFunc(fc *fileContext, cfg Config) []Finding {
	var out []Finding
	for _, d := range fc.file.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		span := fc.line(fn.Body.Rbrace) - fc.line(fn.Body.Lbrace) - 1
		if span > cfg.MaxFuncLines {
			out = append(out, newFinding("long-func", Major, fc.path, fc.line(fn.Pos()),
				"%s is %d body lines (> %d) — extract helpers", fn.Name.Name, span, cfg.MaxFuncLines))
		}
	}
	return out
}
