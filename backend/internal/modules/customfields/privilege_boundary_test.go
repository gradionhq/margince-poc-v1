// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package customfields

// The owner-role downgrade is this package's privilege boundary
// (create.go's Create doc comment): Create and
// SetOptions open their transaction on the schema pool as the owner
// role to run exactly one ALTER TABLE, then MUST drop to margince_app —
// the DML-only role every other tenant write runs under — before the
// catalog INSERT/UPDATE and the audit_log write. Nothing in Go's type
// system pins that ordering, and in dev the schema pool's role is
// superuser, so FORCE RLS does not bite behaviorally if the downgrade
// call is deleted: every existing test still passes green. This mirrors
// backend/writeshape_test.go's approach (fitness function over point
// fix): derive "runs owner DDL, then touches a tenant table" structurally
// — a value literally named "ddl" flowing into tx.Exec/tx.QueryRow marks
// the DDL step, a literal INSERT/UPDATE on custom_field or a
// storekit.Audit/AuditWithEvidence call marks the tenant write — so a
// future DDL-executing function is swept in the moment it exists, and
// neither signal is a string-match on today's function names (Create,
// createInTx, SetOptions, setOptionsInTx never appear below).

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// downgradeFuncName is the one guarded primitive this gate hardcodes —
// analogous to writeshape_test.go hardcoding storekit.Audit/Emit, the
// guarded primitives there.
const downgradeFuncName = "downgradeToAppRole"

func TestSchemaPoolDDLFunctionsDowngradeBeforeTenantWrites(t *testing.T) {
	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	ddlFuncsChecked := 0
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ddlPos, isDDLFunc := firstDDLExecPos(fn.Body)
			if !isDDLFunc {
				continue
			}
			ddlFuncsChecked++

			downgradePos, downgraded := firstCallPos(fn.Body, downgradeFuncName)
			if !downgraded {
				t.Errorf("%s: %s runs owner DDL on the schema pool but never calls %s — "+
					"the transaction stays on the owner role for its catalog/audit writes instead of "+
					"dropping to the DML-only app role",
					path, fn.Name.Name, downgradeFuncName)
				continue
			}
			if downgradePos < ddlPos {
				t.Errorf("%s: %s calls %s before its ALTER — the downgrade must happen AFTER the owner DDL, "+
					"not before it (the ALTER itself needs the owner role)",
					path, fn.Name.Name, downgradeFuncName)
			}
			if dmlPos, hasWrite := firstTenantWritePos(fn.Body); hasWrite && dmlPos < downgradePos {
				t.Errorf("%s: %s writes a tenant table (custom_field/audit_log) before calling %s — "+
					"that write must run under the downgraded app role, exactly like every other tenant "+
					"write, not under the schema-pool owner role",
					path, fn.Name.Name, downgradeFuncName)
			}
		}
	}
	if ddlFuncsChecked == 0 {
		t.Fatal("no schema-pool DDL-executing function found in this package — the derivation heuristic " +
			"is stale (the DDL-then-downgrade shape changed) or every such function was deleted; either way " +
			"this gate needs a look, not a silent pass")
	}
}

// firstDDLExecPos reports whether body calls tx.Exec/tx.QueryRow (any
// receiver — never hardcoded to the name "tx") passing an identifier
// literally named "ddl" as an argument, and if so the earliest such
// call's position. The value is identified by how it is USED — flowing
// into the one raw-SQL execution surface this package runs owner DDL
// through — not by which function produced it or what the enclosing
// function is named, so it survives a rename of BuildDDL/BuildOptionsDDL
// or of Create/SetOptions/createInTx/setOptionsInTx.
func firstDDLExecPos(body *ast.BlockStmt) (token.Pos, bool) {
	var pos token.Pos
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || !isExecLikeCall(call) {
			return true
		}
		for _, arg := range call.Args {
			id, ok := arg.(*ast.Ident)
			if ok && id.Name == "ddl" && (!found || call.Pos() < pos) {
				pos, found = call.Pos(), true
			}
		}
		return true
	})
	return pos, found
}

// firstTenantWritePos finds the earliest call in body that writes a
// tenant table this arc's write shape governs: a literal INSERT/UPDATE
// against custom_field (the catalog table — matched by scanning every
// arg's own literal text, since the INSERT...RETURNING statement is
// built as a string concatenation, not a single literal), or a
// storekit.Audit/AuditWithEvidence call (the audit_log write every
// mutation makes in the same transaction — the same selector match
// backend/writeshape_test.go's paired event gate uses).
func firstTenantWritePos(body *ast.BlockStmt) (token.Pos, bool) {
	var pos token.Pos
	found := false
	consider := func(p token.Pos) {
		if !found || p < pos {
			pos, found = p, true
		}
	}
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if isExecLikeCall(call) && callHasTenantTableLiteral(call) {
			consider(call.Pos())
		}
		if isStorekitAuditCall(call) {
			consider(call.Pos())
		}
		return true
	})
	return pos, found
}

// firstCallPos finds the earliest call to the package-level function
// named name in body.
func firstCallPos(body *ast.BlockStmt, name string) (token.Pos, bool) {
	var pos token.Pos
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok && id.Name == name && (!found || call.Pos() < pos) {
			pos, found = call.Pos(), true
		}
		return true
	})
	return pos, found
}

// isExecLikeCall reports whether call is method.Exec(...) or
// method.QueryRow(...) on any receiver — the two pgx.Tx entry points
// this package runs raw SQL through (QueryRow for the RETURNING insert
// and the row locks, Exec everywhere else). Matching on the method name
// rather than a hardcoded receiver identifier keeps this from silently
// missing a future differently-named tx variable.
func isExecLikeCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	return sel.Sel.Name == "Exec" || sel.Sel.Name == "QueryRow"
}

// callHasTenantTableLiteral reports whether call carries, anywhere in
// its argument expressions (including inside a string concatenation),
// a string literal naming an INSERT or UPDATE against custom_field.
func callHasTenantTableLiteral(call *ast.CallExpr) bool {
	for _, arg := range call.Args {
		found := false
		ast.Inspect(arg, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			sql := strings.ToUpper(lit.Value)
			if strings.Contains(sql, "INSERT INTO CUSTOM_FIELD") || strings.Contains(sql, "UPDATE CUSTOM_FIELD") {
				found = true
			}
			return true
		})
		if found {
			return true
		}
	}
	return false
}

// isStorekitAuditCall reports whether call is storekit.Audit or
// storekit.AuditWithEvidence — the one spelling of the audit_log write
// (the same selector match backend/writeshape_test.go's
// TestEveryAuditedMutationEmitsAnEvent uses for the paired event gate).
func isStorekitAuditCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "storekit" {
		return false
	}
	return sel.Sel.Name == "Audit" || sel.Sel.Name == "AuditWithEvidence"
}
