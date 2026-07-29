// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// The wiring invariant under the outbound-send reconcile, as a fitness function
// rather than a habit: no role this repository assembles builds a delivery
// store without the seam that re-keys a sent message's timeline row.
//
// comms.NewStore accepts a nil reconciler on purpose, so a role that only READS
// deliveries can build one without dragging the activities module in. Nothing
// in the type system separates that role from a transmitting one, and the two
// fail very differently: a transmitting store with no reconciler files every
// message it sends under an identity that exists nowhere on the wire, and
// duplicates it on the timeline, silently.
//
// So the obligation is derived from the tree rather than kept as a list of
// roles — a new binary or worker that composes its own delivery store is caught
// the moment it lands, whether or not anyone remembered this file. Test files
// are out of scope: they build stores for the halves they are about, and one
// suite deliberately passes nil to prove the read path needs no seam.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// identityArg is the position of comms.NewStore's message-identity seam:
// NewStore(pool, now, identity).
const identityArg = 2

// isCommsNewStore reports whether a call expression is comms.NewStore. The
// package qualifier is read off the selector rather than resolved through the
// type checker, which is the same trade the ownership and write-shape gates
// next door make: a local alias of the comms import would slip past, and
// nothing in this tree aliases it.
func isCommsNewStore(fun ast.Expr) bool {
	sel, ok := fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "NewStore" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "comms"
}

func TestEveryComposedDeliveryStoreCarriesAMessageIdentityReconciler(t *testing.T) {
	fset := token.NewFileSet()
	found := 0
	for _, root := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") ||
				strings.HasSuffix(path, "_test.go") || strings.HasSuffix(path, "_gen.go") {
				return err
			}
			file, err := parser.ParseFile(fset, filepath.ToSlash(path), nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || !isCommsNewStore(call.Fun) {
					return true
				}
				found++
				where := fset.Position(call.Pos())
				if len(call.Args) <= identityArg {
					t.Errorf("%s: comms.NewStore takes %d arguments here; the message-identity reconciler is the third",
						where, len(call.Args))
					return true
				}
				if ident, ok := call.Args[identityArg].(*ast.Ident); ok && ident.Name == "nil" {
					t.Errorf("%s: comms.NewStore(…, nil) — a role that transmits without a message-identity reconciler "+
						"files every message it sends under an identity that exists nowhere on the wire, and duplicates it on the timeline",
						where)
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	// A scan that found nothing proves nothing. The product composes delivery
	// stores; a rename or a move that hid every one of them from this walk would
	// otherwise report the invariant as held.
	if found == 0 {
		t.Fatal("no comms.NewStore construction found in the shipped tree — this gate is scanning nothing")
	}
}
