// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// The fitness gate on the boot sequence: a process role that composes
// extensions also records what it composed.
//
// It is derived rather than listed. Naming cmd/api and cmd/worker here would
// leave the next role to be remembered, and the whole failure this guards is a
// role that quietly does not do something — nothing in a boot log says a
// transport went unregistered. Asking "which roles register extensions" of the
// tree means a new one arrives already covered.

import (
	"go/ast"
	"os"
	"path/filepath"
	"testing"
)

// The two compose entry points that must travel together. RegisterExtensions
// declares what this binary composed; RecordComposition is what writes the
// consequences down — the extension inventory, and the channel vocabulary those
// units declare. A role with the first and not the second boots with units
// installed and their transports unregistered, which is the state a captured
// message on such a transport fails on, several layers away from the cause.
const (
	registersExtensions = "RegisterExtensions"
	recordsComposition  = "RecordComposition"
)

func TestEveryRoleThatComposesExtensionsRecordsWhatItComposed(t *testing.T) {
	roles, err := os.ReadDir("cmd")
	if err != nil {
		t.Fatalf("reading the process roles under cmd/: %v", err)
	}
	composing := 0
	for _, role := range roles {
		if !role.IsDir() {
			continue
		}
		called := composeCallsIn(t, filepath.Join("cmd", role.Name()))
		if !called[registersExtensions] {
			continue
		}
		composing++
		if !called[recordsComposition] {
			t.Errorf("cmd/%s registers the composed extension set but never calls compose.%s — "+
				"it boots with units installed and the transports they declare unregistered, "+
				"and nothing in its log says so", role.Name(), recordsComposition)
		}
	}
	if composing == 0 {
		t.Fatal("no process role calls compose." + registersExtensions +
			" — this gate compared nothing, so it cannot have held anything")
	}
}

// composeCallsIn reports which compose.<Name> functions the role's package
// calls. Selector-matched on the `compose` package name, which every role
// imports unaliased; an aliased import would read as no call at all, so the
// zero-roles guard above is what keeps that from passing quietly.
func composeCallsIn(t *testing.T, dir string) map[string]bool {
	t.Helper()
	_, files := parseGoFilesUnder(t, dir)
	called := map[string]bool{}
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "compose" {
				called[sel.Sel.Name] = true
			}
			return true
		})
	}
	return called
}
