// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// The workflow.Handler read/write contract as a fitness function
// (ports/workflow.Handler): Match is a pure predicate and Plan computes
// the typed Effect WITHOUT applying it — "this is what makes dry-run and
// diff preview possible". Only Apply writes, and only through
// ApplyActions / the injected seams. A Match or Plan that mutates breaks
// preview (a dry-run would have a side effect) AND the run lifecycle: a
// pre-Apply failure is claimed terminal (automation/engine_run.go), so a
// write in Match/Plan can commit without a run ever being applied.
//
// This gate scans every production Handler implementation — a receiver
// type whose method set carries the full interface — and fails if either
// its Match or its Plan body contains a write call. Test probes
// (_test.go) are out of scope: they are fixtures, not shipped handlers.
//
// Granularity note: this inspects the Match/Plan method BODIES directly
// (including inline closures), not transitively through helpers. A
// handler that hides a write behind an unexported helper is not caught
// here — but every shipped handler plans inline, and a helper that writes
// would itself be caught by writeshape_test.go's audit/emit gate.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// handlerInterfaceMethods is workflow.Handler's full method set. A
// receiver type that declares all of these IS a Handler (structural /
// duck-typed match, so a new handler is covered the moment it compiles
// against the seam — no registration list to keep in sync).
var handlerInterfaceMethods = []string{"Spec", "Match", "Plan", "Apply", "IdempotencyKey"}

// writeMethodNames are selector calls that mutate: the datasource seam
// (Create/Update/Delete), a raw tx (Exec — Query/QueryRow are reads and
// stay allowed), the approvals/lists/notify seams (Stage/AddMember/
// Notify), and the storekit write shape (Audit/Emit). A read
// (provider.Read, tx.Query, resolver.EffectiveRBAC) names none of these,
// so a Plan that reads a record to decide its effect stays green.
var writeMethodNames = map[string]bool{
	"Create": true, "Update": true, "Delete": true,
	"Exec":  true,
	"Stage": true, "AddMember": true, "Notify": true,
	"Audit": true, "AuditWithEvidence": true, "Emit": true,
}

// writeFuncNames are package-level (non-selector) calls that apply an
// effect. ApplyActions is the executor Apply delegates to — reaching it
// from Match or Plan is the exact confusion this gate exists to stop.
var writeFuncNames = map[string]bool{"ApplyActions": true}

// expectedHandlerTypes are the shipped Handler receiver types this gate
// must actually find and inspect — a WalkDir that silently matched zero
// types would pass vacuously, so the presence check turns "the scan ran"
// into an assertion. New handlers need not be listed (the structural
// match covers them); this list only guards against the scan going blind.
var expectedHandlerTypes = []string{
	"stageChangeCreateTask", "routeLeadCreateTask", "stageChangeNotify", "postMeetingRecap",
	"noActivityReminder", "checkInCadence", "renewalReminder", "leadRouting",
}

func TestWorkflowHandlerMatchAndPlanAreReadOnly(t *testing.T) {
	fset := token.NewFileSet()
	methodsByType := map[string]map[string]bool{}      // "dir.Type" -> method-name set
	matchPlanDecls := map[string][]handlerMethodDecl{} // "dir.Type" -> its Match/Plan decls

	err := filepath.WalkDir("internal/modules", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") ||
			strings.HasSuffix(path, "_test.go") || isIntegrationTagged(path) {
			return err
		}
		path = filepath.ToSlash(path)
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		dir := filepath.ToSlash(filepath.Dir(path))
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil {
				continue
			}
			recv := receiverTypeName(fn)
			if recv == "" {
				continue
			}
			key := dir + "." + recv
			if methodsByType[key] == nil {
				methodsByType[key] = map[string]bool{}
			}
			methodsByType[key][fn.Name.Name] = true
			if (fn.Name.Name == "Match" || fn.Name.Name == "Plan") && fn.Body != nil {
				matchPlanDecls[key] = append(matchPlanDecls[key], handlerMethodDecl{path: path, fn: fn, recv: recv})
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	found := map[string]bool{}
	for key, methods := range methodsByType {
		if !declaresAll(methods, handlerInterfaceMethods) {
			continue
		}
		for _, m := range matchPlanDecls[key] {
			found[m.recv] = true
			for _, w := range writeCallsIn(m.fn.Body) {
				t.Errorf("%s: %s.%s performs a write (%s) — a workflow.Handler's Match/Plan must be read-only. "+
					"Plan computes the Effect WITHOUT applying it (dry-run/preview depend on this); only Apply writes, through ApplyActions/the seams (ports/workflow.Handler).",
					m.path, m.recv, m.fn.Name.Name, w)
			}
		}
	}

	for _, want := range expectedHandlerTypes {
		if !found[want] {
			t.Errorf("handler %q was not discovered by the scan — either it was renamed/moved (update expectedHandlerTypes) or the structural match broke (a Handler no longer declares the full method set)", want)
		}
	}
}

type handlerMethodDecl struct {
	path string
	recv string
	fn   *ast.FuncDecl
}

// receiverTypeName returns the base type name of a method's receiver,
// unwrapping a pointer receiver (*T -> T).
func receiverTypeName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	switch t := fn.Recv.List[0].Type.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		if id, ok := t.X.(*ast.Ident); ok {
			return id.Name
		}
	}
	return ""
}

// declaresAll reports whether the method set contains every required name.
func declaresAll(methods map[string]bool, required []string) bool {
	for _, name := range required {
		if !methods[name] {
			return false
		}
	}
	return true
}

// writeCallsIn returns the distinct write-call names found anywhere in a
// method body (including inline closures). A selector call keys on the
// method name (provider.Update -> "Update"); a bare call keys on the
// function name (ApplyActions).
func writeCallsIn(body *ast.BlockStmt) []string {
	seen := map[string]bool{}
	var out []string
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		var name string
		switch fun := call.Fun.(type) {
		case *ast.SelectorExpr:
			if writeMethodNames[fun.Sel.Name] {
				name = fun.Sel.Name
			}
		case *ast.Ident:
			if writeFuncNames[fun.Name] {
				name = fun.Name
			}
		}
		if name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
		return true
	})
	return out
}
