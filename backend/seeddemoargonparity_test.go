// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build !integration

package backendarch

// seed-demo writes password hashes a REAL person then logs in against, and it
// cannot import the hashing package: that package sits behind a nested
// `internal`, and seed-demo is its own module besides. So the Argon2id
// parameters are restated there, and a restatement drifts.
//
// The comment that used to guard this said a demo user failing to log in was
// the signal. There is no such signal. Verify parses the cost parameters out of
// the PHC hash itself:
//
//	fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p)
//
// so a hash written at ANY parameters verifies successfully, forever, and
// nothing re-hashes on login. Lower seed-demo's constants and every demo user
// signs in perfectly while their stored hash is weak — silently, permanently,
// and invisibly to every other gate in this tree.
//
// That is why this is a gate and not a comment. It reads both files' source
// rather than importing either, which is the only thing the module boundary
// permits, and compares what seed-demo will actually compile against what the
// product ships.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// argonParityFiles: where each half's constants live. params.go carries the
// three cost knobs (build-tagged, so this reads the PRODUCTION file by name
// rather than whatever the current build selected); password.go carries the two
// that are deliberately not tagged.
var argonParityFiles = []string{
	filepath.Join("internal", "modules", "identity", "internal", "password", "params.go"),
	filepath.Join("internal", "modules", "identity", "internal", "password", "password.go"),
}

const seedDemoUsersFile = "tools/seed-demo/users.go"

// argonParityPairs maps the product's constant to seed-demo's name for it.
//
// gatekit:fixture the two names each Argon2 parameter goes by — the product's
// on the left, seed-demo's on the right. These are the subjects this gate
// compares, not costs anybody is excused from: every pair here is CHECKED, and
// a name that stops existing on either side is reported as a failure rather
// than skipped.
var argonParityPairs = map[string]string{
	"timeCost":   "argonTime",
	"memoryKiB":  "argonMemory",
	"threads":    "argonThreads",
	"saltLength": "argonSalt",
	"keyLength":  "argonKey",
}

func TestSeedDemoHashesAtTheParametersTheProductShips(t *testing.T) {
	product := map[string]int64{}
	for _, f := range argonParityFiles {
		for k, v := range intConstants(t, f) {
			product[k] = v
		}
	}
	seed := intConstants(t, seedDemoUsersFile)

	for productName, seedName := range argonParityPairs {
		want, ok := product[productName]
		if !ok {
			t.Errorf("%s is not declared in %v any more — if it was renamed, this gate has to "+
				"be renamed with it or it silently stops comparing anything",
				productName, argonParityFiles)
			continue
		}
		got, ok := seed[seedName]
		if !ok {
			t.Errorf("%s is not declared in %s any more, so nothing pins seed-demo's %s to the "+
				"product's", seedName, seedDemoUsersFile, productName)
			continue
		}
		if got != want {
			t.Errorf("seed-demo hashes at %s=%d where the product ships %s=%d.\n"+
				"A demo user WILL still log in — Verify reads the cost out of the hash, so the "+
				"drift is invisible at the login it would have to break to be noticed. Every hash "+
				"seed-demo has written at %d stays valid and stays weak, because nothing re-hashes "+
				"on login. Set %s to %d in %s.",
				seedName, got, productName, want, got, seedName, want, seedDemoUsersFile)
		}
	}
}

// intConstants returns every untyped integer constant a file declares, with
// `a * b` evaluated — the product writes its memory cost as `19 * 1024` and a
// gate that could not read that would be pinned to the spelling.
func intConstants(t *testing.T, rel string) map[string]int64 {
	t.Helper()
	path := filepath.Join(repoDirForArgonParity(t), rel)
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), path, src, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", rel, err)
	}
	out := map[string]int64{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				if v, ok := evalIntExpr(vs.Values[i]); ok {
					out[name.Name] = v
				}
			}
		}
	}
	return out
}

// evalIntExpr handles the two shapes these constants are written in: a literal,
// and a product of literals. Anything else returns false rather than a wrong
// number — a gate that guessed would be worse than one that says it cannot read
// the value, which is what the missing-constant branch above reports.
func evalIntExpr(e ast.Expr) (int64, bool) {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind != token.INT {
			return 0, false
		}
		n, err := strconv.ParseInt(v.Value, 0, 64)
		return n, err == nil
	case *ast.BinaryExpr:
		if v.Op != token.MUL {
			return 0, false
		}
		l, lok := evalIntExpr(v.X)
		r, rok := evalIntExpr(v.Y)
		return l * r, lok && rok
	}
	return 0, false
}

// repoDirForArgonParity resolves backend/ from the test's own working
// directory, which `go test` sets to the package directory.
func repoDirForArgonParity(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("resolving the working directory: %v", err)
	}
	return wd
}
