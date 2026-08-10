// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

// A batch stager takes its group's row locks up front, or it deadlocks.
//
// Staging inside a loop is what makes an act's lock order the PAYLOAD's order —
// the order a website happened to list its team page in — while a human
// deciding that act's bundle walks the same rows in (created_at, id). Two
// transactions, one shared set, two orders: PostgreSQL breaks the tie by
// aborting one of them, and whichever caller lost sees a 500 on a decision or a
// re-read that was otherwise perfectly valid.
//
// approvals.LockPendingGroupInTx is the fix, and it only works if every batch
// stager calls it. That obligation is derived here rather than kept as a list,
// because the next one will be written by somebody who has never read this
// file: the subject is every function in this package that stages inside a
// loop, taken from the syntax tree, so a new one is covered on the day it
// compiles.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	// stageInTx is the in-transaction staging call. Only this one: the pooled
	// Stage opens its own transaction per proposal, so a loop over it takes one
	// lock at a time and releases it — there is no set to order.
	stageInTx = "StageOrJoinPendingInTx"
	// groupLock is what such a loop owes before its first iteration.
	groupLock = "LockPendingGroupInTx"
)

// The floor that stops this certifying nothing: the package really does stage
// in a loop, in more than one place, and a walk that finds none has broken
// rather than been satisfied.
const batchStagerFloor = 2

func TestEveryBatchStagerPreLocksItsGroup(t *testing.T) {
	decls := packageFuncs(t)
	staging := stagingFuncs(decls)
	found := 0
	for _, fn := range decls {
		if !stagesInsideALoop(fn.decl.Body, staging) {
			continue
		}
		found++
		if callsAny(fn.decl.Body, map[string]bool{groupLock: true}) {
			continue
		}
		t.Errorf("%s (%s) stages inside a loop without calling %s first. Its locks are then taken "+
			"in the payload's order, which nothing makes agree with the (created_at, id) order a "+
			"bundle decision walks the same rows in — so the two deadlock and one of them 500s. "+
			"Take the group's locks up front.", fn.decl.Name.Name, filepath.Base(fn.file), groupLock)
	}
	if found < batchStagerFloor {
		t.Fatalf("found %d functions staging inside a loop, expected at least %d — the AST walk "+
			"broke rather than the subject, and a gate with no subjects certifies nothing",
			found, batchStagerFloor)
	}
}

// stagingFuncs names the in-transaction staging call plus the per-member
// helpers that make it directly.
//
// ONE level of indirection, not a transitive closure. That is the shape the
// subject actually has — one batch stager calls the seam inside its own loop,
// the other calls a helper that stages one member — and it is the shape that
// stays honest: following the call graph further marks a batch stager itself as
// "staging", so every job that loops over workspaces calling one is reported as
// a batch stager too, and the gate drowns its own finding in a page of work
// that takes no approval lock at all.
func stagingFuncs(decls []packageFunc) map[string]bool {
	staging := map[string]bool{stageInTx: true}
	direct := map[string]bool{stageInTx: true}
	for _, fn := range decls {
		if callsAny(fn.decl.Body, direct) {
			staging[fn.decl.Name.Name] = true
		}
	}
	return staging
}

// stagesInsideALoop reports whether the body contains a loop that stages on a
// caller-owned transaction. Nested function literals count: a loop whose body
// is a closure is the same transaction and the same lock sequence.
func stagesInsideALoop(body *ast.BlockStmt, staging map[string]bool) bool {
	if body == nil {
		return false
	}
	inLoop := false
	ast.Inspect(body, func(n ast.Node) bool {
		switch loop := n.(type) {
		case *ast.RangeStmt:
			inLoop = inLoop || callsAny(loop.Body, staging)
		case *ast.ForStmt:
			inLoop = inLoop || callsAny(loop.Body, staging)
		}
		return !inLoop
	})
	return inLoop
}

// callsAny reports whether the node contains a call to any of these names,
// whatever the receiver is spelled as — the seam is the name, and the service
// reaches these call sites under several field names. A plain call (a package
// helper) counts as well as a selector one (a method).
func callsAny(n ast.Node, names map[string]bool) bool {
	if n == nil {
		return false
	}
	called := false
	ast.Inspect(n, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.SelectorExpr:
			called = called || names[fun.Sel.Name]
		case *ast.Ident:
			called = called || names[fun.Name]
		}
		return !called
	})
	return called
}

// packageFunc is one declared function and the file it was read from, so a
// finding can name where to go.
type packageFunc struct {
	decl *ast.FuncDecl
	file string
}

// packageFuncs parses this package's hand-written Go files, non-recursively: a
// subpackage is a different composition unit with its own transactions.
func packageFuncs(t *testing.T) []packageFunc {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}
	fset := token.NewFileSet()
	var funcs []packageFunc
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, e.Name(), nil, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", e.Name(), err)
		}
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
				funcs = append(funcs, packageFunc{decl: fn, file: e.Name()})
			}
		}
	}
	return funcs
}
