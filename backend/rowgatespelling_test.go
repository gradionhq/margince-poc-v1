// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// A module's READ spelling of a row gate is not a licence to write through it.
//
// writeauthority_test.go asks the tier-wide question — is each probe on a
// mutating path the write-authority spelling — and ratifies the shared read
// helpers by NAME, because a helper like deals' visibleOffer really is a read
// probe wherever it is called from. That is what makes this gate its
// complement rather than a second copy: the waiver is keyed on the function
// HOLDING the probe, so once a package's read spelling is ratified, a NEW
// write that resolves its row through it inherits the waiver and the tier gate
// reads green over it. The class is not hypothetical. An offer's PDF render
// persisted its blob ref through the read spelling while every other offer
// write took the write one, so any seat that could SEE a deal could overwrite
// and destroy the offer PDF hanging off it.
//
// The population is derived from the tree, so a module that grows the pattern
// inherits the obligation rather than being added to a list:
//
//   - a package's READ spelling is a function whose record-authority probes are
//     visibility ones only;
//   - it has a WRITE twin when some function in the same package calls it and
//     adds a write-authority probe. That pairing is the package's own statement
//     that the two authorities differ for its rows, so a package without one is
//     out of scope rather than failed;
//   - the OBLIGATION is that a function reaching the storekit write shape does
//     not call the read spelling directly. It goes through the twin.
//
// The obligation deliberately admits no "unless this function takes the probe
// itself" exemption. A frame-level one cannot tell WHICH row the probe
// authorized, so it would excuse the very shape this exists to catch: a
// function that correctly gates row A, then resolves row B through the read
// spelling and writes B. Nothing in the tier needs the exemption — every
// writer already goes through its package's twin — so the strict rule costs
// nothing today, and the first genuine exception argues for itself in review
// rather than passing in silence.

import (
	"go/ast"
	"path/filepath"
	"testing"
)

// rowGateWriteMarkers witness that a function reaches a WRITE. It is
// mutationMarkers without the lock family: taking a row lock is how the
// mutation SPELLINGS themselves are built, so counting a lock as a write would
// make every one of them its own violation.
var rowGateWriteMarkers = map[string]bool{
	"Audit": true, "AuditWithEvidence": true, "Emit": true, "StampFields": true,
	"ApplyWithVersion": true, "ApplyGuarded": true, "ApplyLocked": true,
}

// rowGateFn is what this gate needs about one function declaration: which
// authority it probes for, whether it writes, and who it calls.
type rowGateFn struct {
	name       string
	file       string
	line       int
	visibility bool
	authority  bool
	writes     bool
	calls      map[string]bool
}

func TestNoWriteResolvesItsRowThroughAPackagesReadSpelling(t *testing.T) {
	byDir := rowGateIndex(t)
	pairs := 0
	for _, fns := range byDir {
		readSpellings := readSpellingsOf(fns)
		twinned := twinnedReadSpellings(fns, readSpellings)
		pairs += len(twinned)
		for _, fn := range fns {
			if !fn.writes {
				continue
			}
			for call := range fn.calls {
				twin, isReadSpelling := twinned[call]
				if !isReadSpelling {
					continue
				}
				t.Errorf("%s:%d: %s writes after resolving its row through %s, this package's READ spelling — "+
					"a manual grant widens VISIBILITY at either access level, so this admits a caller holding only "+
					"a `read` share; resolve the row through %s, the write-authority twin this package already has",
					fn.file, fn.line, fn.name, call, twin)
			}
		}
	}
	if pairs == 0 {
		t.Fatalf("no read spelling with a write-authority twin found in %s — the extractor lost its source", modulesDir)
	}
}

// rowGateIndex parses the module tier into per-directory function declarations.
// Declarations are kept individually rather than merged by name: a handler and
// a store in one package routinely spell the same method name, and merging
// would let one function's write answer for another's probe.
func rowGateIndex(t *testing.T) map[string][]*rowGateFn {
	t.Helper()
	byDir := map[string][]*rowGateFn{}
	for _, src := range tierFiles(t, modulesDir) {
		dir := filepath.ToSlash(filepath.Dir(src.Path))
		for _, decl := range src.File.Decls {
			fn, isFunc := decl.(*ast.FuncDecl)
			if !isFunc || fn.Body == nil {
				continue
			}
			indexed := &rowGateFn{
				name: fn.Name.Name, file: src.Path, line: src.Line(fn.Pos()),
				calls: map[string]bool{},
			}
			indexRowGateBody(fn, indexed)
			byDir[dir] = append(byDir[dir], indexed)
		}
	}
	return byDir
}

func indexRowGateBody(fn *ast.FuncDecl, into *rowGateFn) {
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.CallExpr:
			switch fun := n.Fun.(type) {
			case *ast.Ident:
				into.calls[fun.Name] = true
			case *ast.SelectorExpr:
				if pkg, isPkg := fun.X.(*ast.Ident); isPkg && pkg.Name == "auth" {
					into.visibility = into.visibility || recordAuthorityProbes[fun.Sel.Name]
					into.authority = into.authority || writeAuthorityProbes[fun.Sel.Name]
					return true
				}
				into.writes = into.writes || rowGateWriteMarkers[fun.Sel.Name]
				into.calls[fun.Sel.Name] = true
			}
		case *ast.BasicLit:
			if text, isString := stringConst(n); isString && writesSQL(text) {
				into.writes = true
			}
		}
		return true
	})
}

// readSpellingsOf names the functions whose record-authority probes are
// visibility ones only. A name declared more than once in a package qualifies
// only when EVERY declaration of it does: the gate cannot tell which one a call
// meant, so a name that is a read probe in one place and a write probe in
// another is treated as neither rather than guessed at.
func readSpellingsOf(fns []*rowGateFn) map[string]bool {
	reads, disqualified := map[string]bool{}, map[string]bool{}
	for _, fn := range fns {
		if fn.authority || !fn.visibility {
			disqualified[fn.name] = true
			continue
		}
		reads[fn.name] = true
	}
	for name := range disqualified {
		delete(reads, name)
	}
	return reads
}

// twinnedReadSpellings narrows the read spellings to those the package itself
// has already paired with a write-authority probe, and answers each one's
// TWIN — the pairing IS the package's statement that the two authorities
// differ for its rows, and naming the twin is what lets a failure say which
// function to route through rather than leaving the author to find it.
//
// A read spelling with more than one twin keeps the first in the tier walk's
// file-and-declaration order, so the message a given tree produces is stable:
// any twin proves the pairing, and the diagnostic needs one to point at, not a
// census of them.
func twinnedReadSpellings(fns []*rowGateFn, reads map[string]bool) map[string]string {
	twinned := map[string]string{}
	for _, fn := range fns {
		if !fn.authority {
			continue
		}
		for call := range fn.calls {
			if _, known := twinned[call]; reads[call] && !known {
				twinned[call] = fn.name
			}
		}
	}
	return twinned
}
