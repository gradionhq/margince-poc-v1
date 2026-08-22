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

	"github.com/gradionhq/margince/backend/internal/shared/gatekit"
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

// transportHelpers are the Handlers methods declared outside handlers_public.go
// that a public handler may still call, each with why it carries no buyer
// authority. They exist for the link request alone, which runs under the
// installation's own principal after it has answered the anonymous caller.
var transportHelpers = gatekit.Waive(map[string]string{
	"canSendInvite":     "reads two fields of the handler set; touches no store",
	"sendInvite":        "hands an already-minted credential to the relay; touches no store",
	"recordSendOutcome": "writes the relay's verdict through RecordInvitationSend under linkRequestPrincipal, a system actor the seat gate admits; the buyer principal never reaches it",
})

func TestPublicHandlersReachOnlyTheSessionScopedStore(t *testing.T) {
	defer transportHelpers.AssertAllMatched(t)
	allowedStore, _ := publicStoreMethods(t)
	f := parseGoFile(t, publicHandlersFile)
	declaredHere := map[string]bool{}
	for _, decl := range f.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv != nil {
			declaredHere[fn.Name.Name] = true
		}
	}
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
		// h.store.X — the store boundary.
		if inner, ok := sel.X.(*ast.SelectorExpr); ok && inner.Sel.Name == "store" {
			if !allowedStore[sel.Sel.Name] {
				violations = append(violations, "h.store."+sel.Sel.Name)
			}
			return true
		}
		// h.X — an indirection that could reach the seller's store unseen.
		if recv, ok := sel.X.(*ast.Ident); ok && recv.Name == "h" {
			if !declaredHere[sel.Sel.Name] && !transportHelpers.Waived(t, sel.Sel.Name) {
				violations = append(violations, "h."+sel.Sel.Name)
			}
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
		// Per STATEMENT: every raw-string literal that reads or writes one of
		// the session-scoped tables carries a room predicate of its own, so a
		// predicated neighbour in the same file cannot vouch for it.
		// The exchange predicate is a named constant spliced into two statements;
		// fold it back in so each statement is judged whole.
		text = strings.ReplaceAll(text, "`+exchangeablePredicate+`", exchangeablePredicate)
		// The address lookup splices the roster's FROM clause in through %s;
		// fold that in too, so its statement is judged on its own predicate.
		text = strings.ReplaceAll(text, "participantColumns, participantFrom,", "")
		text = strings.ReplaceAll(text, "FROM %s", "FROM "+participantFrom)
		parts := strings.Split(text, "`")
		for i := 1; i < len(parts); i += 2 {
			stmt := parts[i]
			for _, table := range []string{"deal_room_session", "deal_room_participant", "deal_room_release"} {
				if !strings.Contains(stmt, "FROM "+table) && !strings.Contains(stmt, "UPDATE "+table) {
					continue
				}
				// p.email = $ is the one legitimately cross-room predicate: the
				// link request finds every seat an ADDRESS holds, and mails
				// that address alone.
				if !strings.Contains(stmt, "room_id = $") && !strings.Contains(stmt, "room_id = r.id") && !strings.Contains(stmt, "room_id = s.room_id") && !strings.Contains(stmt, "participant_id = $") && !strings.Contains(stmt, "token_hash = $") && !strings.Contains(stmt, "p.email = $") {
					t.Errorf("%s: a statement reads %s without a room, participant or token predicate:\n%s", file, table, stmt)
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
