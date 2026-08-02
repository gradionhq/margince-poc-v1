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
func TestOnlyTheSharedHelperBindsAWorkspace(t *testing.T) {
	fset, files := parseGoFilesUnder(t, filepath.Join("internal", "compose"))
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
}
