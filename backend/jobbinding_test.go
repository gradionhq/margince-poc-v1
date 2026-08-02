// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

import (
	"go/ast"
	"path/filepath"
	"testing"
)

// TestOnlyTheSharedHelperBindsAWorkspace pins the OTHER half of the role
// contract: a declaration only governs if it is what actually binds the
// GUC. River's WorkerMiddleware sees a *rivertype.JobRow (raw JSON), never
// the typed args, so the binding cannot live there — it lives in compose's
// workWorkspace helper, and this gate is what keeps it the only site.
//
// A worker that bound its own workspace from job.Args could declare one
// field and bind another, or bind a zero UUID, with the role gate above
// still green. That is the drift this prevents.
// workspaceBindFloor guards against a vacuous pass. This gate is a
// PROHIBITION, so "found nothing" is what success looks like — which means it
// would also read green if the walker silently matched no files at all. The
// floor counts the POSITIVE side instead: how many Work methods reach the
// shared guard. Every workspace-scoped kind routes through it, so a walk that
// stopped working would fail here rather than pass quietly.
const workspaceBindFloor = 15

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
