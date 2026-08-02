// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

import (
	"go/ast"
	"path/filepath"
	"testing"
)

// workspaceBindFloor guards against a vacuous pass. This gate is a
// PROHIBITION, so "found nothing" is what success looks like — which means it
// would also read green if the walker silently matched no files at all. The
// floor counts the POSITIVE side instead: Work methods that reach
// workspaceJobCtx at all.
//
// It counts REACHES, not "binds through": several workers call the helper for
// its zero-id refusal and then bind through a per-kind scope helper that takes
// the workspace as a parameter. Those two values agree today because both come
// from the same args field, but nothing here asserts that — see the follow-up
// note in the job-observability ledger.
const workspaceBindFloor = 15

// TestOnlyTheSharedHelperBindsAWorkspace pins the OTHER half of the role
// contract: a declaration only governs if it is what actually binds the GUC.
// River's WorkerMiddleware sees a rivertype.JobRow — raw JSON, never the typed
// args — so the binding cannot live there. It lives in compose's
// workspaceJobCtx, and this gate keeps a Work body from binding inline beside
// it.
//
// A worker that bound its own workspace from job.Args could declare one field
// and bind another, or bind a zero UUID, with the role gate in jobrole_test.go
// still green. That is the drift this prevents.
func TestOnlyTheSharedHelperBindsAWorkspace(t *testing.T) {
	fset, files := parseGoFilesUnder(t, filepath.Join("internal", "compose"))
	guarded := 0
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			if fn.Recv == nil || fn.Name.Name != "Work" {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "workspaceJobCtx" {
					guarded++
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, ok := sel.X.(*ast.Ident)
				if !ok || pkg.Name != "principal" || sel.Sel.Name != "WithWorkspaceID" {
					return true
				}
				pos := fset.Position(call.Pos())
				t.Errorf("%s:%d: a Work method binds its own workspace. Bind through workspaceJobCtx(ctx, job.Args) so the args' own WorkspaceID() declaration IS the binding — a worker that picks its own can claim one workspace and work in another.",
					pos.Filename, pos.Line)
				return false
			})
		}
	}
	if guarded < workspaceBindFloor {
		t.Fatalf("only %d Work methods reach workspaceJobCtx, expected at least %d — the walker matched almost nothing and this prohibition would pass vacuously",
			guarded, workspaceBindFloor)
	}
}
