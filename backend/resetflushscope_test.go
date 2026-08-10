// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// The reset's cache flush has two entry points on purpose, and the split is a
// security boundary rather than a style choice.
//
// Server.FlushResetCaches is what the reset control channel reaches. That
// channel is Redis pub/sub carrying no signature and no provenance, so anyone
// who can reach the bus can publish a workspace id and land in it, having
// passed none of the gates the reset endpoint enforces (non-production posture,
// human-only, admin-only, typed confirmation). Everything reachable from it
// must therefore be safe for an unauthenticated caller: dropping a cached
// answer costs a recomputation and nothing else.
//
// Clearing the auth lockout buckets is not that. They are brute-force brakes —
// a login attempt costs a full Argon2id verification, a reset request costs an
// outbound email — so reopening them belongs only to flushAfterOwnReset, the
// path that runs inside the gated handler.
//
// The two are one line apart and read almost identically, which is exactly why
// this is asserted rather than left to a comment: folding them back together
// would hand an unauthenticated publisher a way to reopen those budgets, and
// nothing else in the tree would notice.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

const (
	resetFlushFile   = "internal/compose/datareset_runtime.go"
	busReachedFlush  = "FlushResetCaches"
	gatedFlush       = "flushAfterOwnReset"
	lockoutResetCall = "ResetRateLimits"
)

func TestOnlyTheGatedResetFlushClearsTheAuthLockouts(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, resetFlushFile, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", resetFlushFile, err)
	}

	bodies := map[string]string{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		var sb strings.Builder
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				sb.WriteString(sel.Sel.Name)
				sb.WriteString(" ")
			}
			return true
		})
		bodies[fn.Name.Name] = sb.String()
	}

	busBody, ok := bodies[busReachedFlush]
	if !ok {
		t.Fatalf("%s no longer declares %s — if the flush moved, move this gate with it",
			resetFlushFile, busReachedFlush)
	}
	gatedBody, ok := bodies[gatedFlush]
	if !ok {
		t.Fatalf("%s no longer declares %s — if the gated flush moved, move this gate with it",
			resetFlushFile, gatedFlush)
	}

	if strings.Contains(busBody, lockoutResetCall) {
		t.Errorf("%s calls %s, so an unauthenticated publisher on the reset control channel "+
			"can reopen the auth lockout buckets without passing a single gate; "+
			"that call belongs in %s", busReachedFlush, lockoutResetCall, gatedFlush)
	}
	if !strings.Contains(gatedBody, lockoutResetCall) {
		t.Errorf("%s no longer calls %s, so the admin who just wiped the installation "+
			"stays locked out by counters the wipe could not reach", gatedFlush, lockoutResetCall)
	}
}
