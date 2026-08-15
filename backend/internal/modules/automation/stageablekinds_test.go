// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package automation

// StageableKinds is the list every approval-lifecycle gate reads, and every one
// of those gates checks the list rather than the code. They prove that what is
// LISTED is decidable and executable; none of them can notice a kind that
// starts staging and is never added. That gap is the blind spot the whole area
// exists to close — two kinds were undecidable for exactly as long as nothing
// looked at them.
//
// The staged KIND cannot be derived from the switch, and that is worth stating
// plainly rather than pretending otherwise: stageForApproval takes it from
// action.Kind at run time, and the draft_email arm stages under held_draft
// rather than under its own name. So this is a tripwire, not a derivation — it
// scans applyOne for the arms that reach a staging call, directly or through a
// helper, and fails when that set changes. A new staging arm therefore cannot
// land without somebody revisiting StageableKinds, which is the only failure
// mode the other gates cannot see.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// stagingCalls are the functions that put a card in front of a human. A new one
// added to applyOne without being named here would be missed, which is why the
// test below also fails when it finds no staging calls at all.
var stagingCalls = map[string]bool{"stageForApproval": true, "stageHeldDraft": true}

// gatekit:fixture the switch arms known to stage, and the approval kind each produces
//
// stagingArms are the switch arms of applyOne known to stage, each named by the
// action that reaches the staging call and by the approval kind it produces.
// The two differ where the mapping is not the identity, which is the reason
// this list is written out rather than computed.
var stagingArms = map[string]string{
	"ActionEmitFlowEvent": "emit_flow_event", // request_approval's executor
	"ActionAssignOwner":   "assign_owner",    // through applyAssignOwner, on its 🟡 branch only
	"ActionDraftEmail":    "held_draft",      // stages the SEND it proposes, not itself
}

func TestStageableKindsMatchesTheStagingSwitch(t *testing.T) {
	fset := token.NewFileSet()
	// Every file, not engine.go alone: the helper that stages assign_owner lives
	// in handlers_actions.go, and a scan that missed it would report the arm as
	// no longer staging — a false green on the one kind whose staging is
	// indirect.
	staged := kindsStagedByApplyOne(t, parsePackage(t, fset))
	if len(staged) == 0 {
		t.Fatal("found no staging call in applyOne — the scan is broken, not the code")
	}

	for arm := range staged {
		if _, known := stagingArms[arm]; !known {
			t.Errorf("applyOne stages on %s and nothing here maps it to an approval kind — every gate over the approval lifecycle reads StageableKinds(), so a kind reaching the inbox by this arm is checked by nothing",
				arm)
		}
	}
	for arm := range stagingArms {
		if !staged[arm] {
			t.Errorf("%s is recorded as a staging arm but applyOne no longer stages on it — a gate is holding a kind that cannot occur, which reads as coverage it does not have",
				arm)
		}
	}

	declared := map[string]bool{}
	for _, kind := range StageableKinds() {
		declared[kind] = true
	}
	for arm, kind := range stagingArms {
		if !declared[kind] {
			t.Errorf("%s stages %q and StageableKinds() omits it", arm, kind)
		}
		delete(declared, kind)
	}
	for kind := range declared {
		t.Errorf("StageableKinds() lists %q but no arm of applyOne stages it", kind)
	}
}

// kindsStagedByApplyOne collects the case-arm identifiers of every switch arm
// whose body reaches a staging call.
func kindsStagedByApplyOne(t *testing.T, files []*ast.File) map[string]bool {
	t.Helper()
	staged := map[string]bool{}
	// One level of indirection: assign_owner stages inside applyAssignOwner
	// rather than in its own arm, so a scan that only looked at arm bodies
	// would miss the kind whose 🟡 branch is the whole reason this exists.
	stagers := map[string]bool{}
	for name := range stagingCalls {
		stagers[name] = true
	}
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if ok && fn.Body != nil && bodyStages(fn.Body.List, stagingCalls) {
				stagers[fn.Name.Name] = true
			}
			return true
		})
	}
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "applyOne" {
				return true
			}
			ast.Inspect(fn.Body, func(inner ast.Node) bool {
				clause, ok := inner.(*ast.CaseClause)
				if !ok || !bodyStages(clause.Body, stagers) {
					return true
				}
				for _, expr := range clause.List {
					staged[lastIdent(expr)] = true
				}
				return true
			})
			return false
		})
	}
	return staged
}

// bodyStages reports whether this arm calls one of the staging functions.
func bodyStages(body []ast.Stmt, stagers map[string]bool) bool {
	found := false
	for _, stmt := range body {
		ast.Inspect(stmt, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if ident, ok := call.Fun.(*ast.Ident); ok && stagers[ident.Name] {
				found = true
			}
			return true
		})
	}
	return found
}

// lastIdent names an expression the way the switch writes it:
// workflow.ActionAssignOwner -> "ActionAssignOwner", HeldDraftKind -> "HeldDraftKind".
func lastIdent(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.SelectorExpr:
		return e.Sel.Name
	case *ast.Ident:
		return e.Name
	default:
		return ""
	}
}

// parsePackage reads the package's own (non-test) sources.
//
// os.ReadDir + ParseFile rather than parser.ParseDir: that helper and ast.Package
// are both deprecated, and the build-tag subtlety they were deprecated for is
// exactly the kind of thing that would make this scan quietly cover less than it
// claims.
func parsePackage(t *testing.T, fset *token.FileSet) []*ast.File {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}
	var files []*ast.File
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		files = append(files, file)
	}
	if len(files) == 0 {
		t.Fatal("no source files parsed — the scan is broken, not the code")
	}
	return files
}
