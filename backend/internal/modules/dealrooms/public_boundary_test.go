// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dealrooms

// The buyer edge's authority boundary, held as a fitness function over the
// source rather than a comment. A buyer's only authority is the session, and
// the seller's store methods gate on a seat the buyer does not hold — so the
// public handlers must reach the store ONLY through the session-scoped methods,
// and those methods must never consult the seat gates. The obligation is
// derived from the files: every `h.store.X` call in handlers_public.go is
// resolved against the method set declared in store_public*.go.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const publicHandlersFile = "handlers_public.go"

// publicStoreMethods lists the receiver methods declared across the
// store_public*.go files — the only methods a public handler may call.
func publicStoreMethods(t *testing.T) (map[string]bool, []string) {
	t.Helper()
	files, err := filepath.Glob("store_public*.go")
	if err != nil {
		t.Fatal(err)
	}
	methods := map[string]bool{}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		f := parseGoFile(t, file)
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil {
				continue
			}
			methods[fn.Name.Name] = true
		}
	}
	if len(methods) == 0 {
		t.Fatal("no session-scoped store methods found; the glob store_public*.go matched nothing")
	}
	return methods, files
}

func parseGoFile(t *testing.T, file string) *ast.File {
	t.Helper()
	src, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	f, err := parser.ParseFile(token.NewFileSet(), file, src, 0)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestPublicHandlersReachOnlyTheSessionScopedStore(t *testing.T) {
	allowed, _ := publicStoreMethods(t)
	f := parseGoFile(t, publicHandlersFile)
	var violations []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		inner, ok := sel.X.(*ast.SelectorExpr)
		if !ok || inner.Sel.Name != "store" {
			return true
		}
		if !allowed[sel.Sel.Name] {
			violations = append(violations, sel.Sel.Name)
		}
		return true
	})
	if len(violations) > 0 {
		t.Fatalf("handlers_public.go calls store methods outside store_public*.go: %v\n"+
			"a seller method gates on a seat the buyer does not hold; move the read into a session-scoped method",
			violations)
	}
}

// Every session-scoped store method must carry the session's predicate and
// none may consult the seat gates. Checked textually per file: the seat gates
// are named package functions, and the predicate is a column in a WHERE clause.
func TestSessionScopedStoreNeverConsultsTheSeatGates(t *testing.T) {
	_, files := publicStoreMethods(t)
	for _, file := range files {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		text := string(src)
		for _, gate := range []string{"auth.Require(", "auth.RequireHuman(", "auth.EnsureVisible(", "auth.EnsureWritable(", "dealScopeClause(", "readRoom("} {
			if strings.Contains(text, gate) {
				t.Errorf("%s calls %s: the seat gates refuse a buyer and the deal-scoped room read assumes one; a buyer's authority is the session predicate", file, strings.TrimSuffix(gate, "("))
			}
		}
		for _, table := range []string{"deal_room_task", "deal_room_session", "deal_room_participant"} {
			if strings.Contains(text, "FROM "+table) || strings.Contains(text, "UPDATE "+table) {
				if !strings.Contains(text, "room_id = $") && !strings.Contains(text, "room_id = r.id") {
					t.Errorf("%s reads %s without a room predicate", file, table)
				}
			}
		}
	}
}

// The handlers file itself must not read the seller's gates or the deal either:
// a public handler holds no SQL and no authority of its own.
func TestPublicHandlersHoldNoAuthorityOfTheirOwn(t *testing.T) {
	src, err := os.ReadFile(publicHandlersFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"auth.", "tx.", "pgx.", "SELECT ", "UPDATE "} {
		if strings.Contains(string(src), forbidden) {
			t.Errorf("handlers_public.go contains %q: the buyer edge's transport holds no SQL and consults no seat gate", forbidden)
		}
	}
}
