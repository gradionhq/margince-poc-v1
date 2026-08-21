// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// agent_run.degrade_reason is served to the human the run acted for
// (GET /me/agent-activity forwards it verbatim), and this file is where every
// out-of-loop close of a run is decided. Twice already a reason was built at the
// call site from the error that caused it — an identity module's message, a
// wrapped internal fault — and both went straight to a rep's browser.
//
// runner.FailureReason stops the two mechanisms that did it: err.Error() and any
// prefix concatenated onto it are typed strings and no longer convert. What it
// cannot stop is a bare string LITERAL, which is untyped and converts happily,
// and a literal at a call site is how a reason drifts out of the vocabulary and
// starts describing a cause again. So the obligation is read off the source: the
// reason is always a NAME, and the vocabulary is therefore enumerable in one
// place instead of scattered across the sites that close a run.
func TestEveryRunCloseHereNamesAReasonFromTheVocabulary(t *testing.T) {
	const file = "runnerservice.go"
	// The reason is the third argument of both closers — MarkFailed(ctx, run,
	// reason) and FailStuckRuns(ctx, grace, reason).
	const reasonArg = 2
	closers := map[string]bool{"MarkFailed": true, "FailStuckRuns": true}

	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, file, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}

	found := 0
	ast.Inspect(parsed, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !closers[sel.Sel.Name] || len(call.Args) <= reasonArg {
			return true
		}
		found++
		switch call.Args[reasonArg].(type) {
		case *ast.Ident, *ast.SelectorExpr:
			// A named constant: runner.FailureX, or this file's own.
		default:
			t.Errorf("%s: the %s at %s builds its reason at the call site instead of naming one — "+
				"a reason assembled here is how a cause reached the column the reader sees twice before. "+
				"Log the cause and pass a runner.FailureReason constant",
				file, sel.Sel.Name, fset.Position(call.Args[reasonArg].Pos()))
		}
		return true
	})
	if found == 0 {
		t.Fatalf("%s closes no run through MarkFailed or FailStuckRuns — this gate is reading the "+
			"wrong file, which is worse than not having it", file)
	}
}
