// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package backendarch

// The Art. 7(1) demonstrability invariant as a fitness function: every
// write that sets a person_consent STATE appends a consent_event proof
// row in the same function (data-model §3.4 — the current state is
// always backed by an append-only event saying when, how, and by whom).
// Subject repoints (SET person_id, the merge/promotion carry-through)
// and row deletions are not state changes and are out of scope by
// construction. A state write without proof silently voids the
// workspace's ability to demonstrate consent; this gate keeps any
// module — consent's siblings included — from reintroducing one.
//
// Exceptions are explicit, keyed by package path + function, each with
// the rationale that ratified it; a reasonless or stale waiver fails.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// consentStateWrite matches an INSERT into person_consent (every insert
// carries a state, explicit or defaulted) or an UPDATE that sets its
// state column, inside one SQL string literal.
var consentStateWrite = regexp.MustCompile(`(?is)(?:INSERT\s+INTO\s+person_consent\b|UPDATE\s+person_consent\b.*\bSET\b[^;]*\bstate\s*=)`)

// consentProofInsert witnesses the paired append-only proof row.
var consentProofInsert = regexp.MustCompile(`(?is)INSERT\s+INTO\s+consent_event\b`)

// unprovenConsentWrites are the ratified proof-free state writes, keyed
// by "package-dir:FuncName" with the rationale inline.
var unprovenConsentWrites = map[string]string{}

func TestEveryConsentStateWriteAppendsProof(t *testing.T) {
	for fn, rationale := range unprovenConsentWrites {
		if strings.TrimSpace(rationale) == "" {
			t.Errorf("unprovenConsentWrites[%s] has no rationale — a waiver must say why the proof row is missing", fn)
		}
	}
	used := map[string]bool{}
	fset := token.NewFileSet()
	for _, root := range []string{"internal/modules", "internal/compose"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") ||
				isIntegrationTagged(path) {
				return err
			}
			path = filepath.ToSlash(path)
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				var writesState, appendsProof bool
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					lit, ok := n.(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						return true
					}
					sql, err := strconv.Unquote(lit.Value)
					if err != nil {
						return true
					}
					if consentStateWrite.MatchString(sql) {
						writesState = true
					}
					if consentProofInsert.MatchString(sql) {
						appendsProof = true
					}
					return true
				})
				if writesState && !appendsProof {
					key := filepath.ToSlash(filepath.Dir(path)) + ":" + fn.Name.Name
					if _, ratified := unprovenConsentWrites[key]; ratified {
						used[key] = true
						continue
					}
					t.Errorf("%s: %s writes a person_consent state without appending a consent_event — every state change carries its Art. 7(1) proof (data-model §3.4), or the exception is ratified in unprovenConsentWrites",
						path, fn.Name.Name)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for key := range unprovenConsentWrites {
		if !used[key] {
			t.Errorf("unprovenConsentWrites[%s] matches no proof-free consent state write — stale waiver, remove it", key)
		}
	}
}
