// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package approvals

// A lock order is only a rule if every locker follows it.
//
// The deadlock this guards is not a race one reviewer can see in one file: it
// needs two statements, usually in two files, that lock a shared set of
// `approval` rows in different orders. Each is correct read alone, which is why
// the obligation is derived from the package's own SQL rather than kept as a
// list somebody remembers to extend — a new locker added tomorrow is covered on
// the day it is written.
//
// The subject is source TEXT rather than a walk over string literals, because
// the order is spelled by concatenating the lockOrder constant into the query:
// a literal-by-literal walk sees the two halves separately and the order in
// neither. It is the text of the parsed file with COMMENTS DROPPED, though —
// this package explains its locking at length, and prose describing a
// `SELECT ... FOR UPDATE` is not a statement that takes one.

import (
	"bytes"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// lockingStatement captures one row-locking statement — from its verb to its
// FOR UPDATE — so each is judged on its own text. Non-greedy, so two statements
// in one file are two matches rather than one span swallowing both.
var lockingStatement = regexp.MustCompile(`(?s)\b(?:SELECT|UPDATE)\b.*?FOR UPDATE`)

// singleRowLock recognises the shapes that can only ever lock ONE row. A single
// lock has no order to get wrong: an order exists between rows, and there is no
// second row. Both spellings are in use — by primary key, and by a bounded
// pick-the-newest probe.
var singleRowLock = regexp.MustCompile(`(?s)WHERE id = \$\d\b|LIMIT 1`)

// The floor that keeps this from certifying nothing: the package really does
// lock rows, in more places than one, and a scan that suddenly finds none has
// broken rather than been satisfied.
const lockingStatementFloor = 4

func TestEveryMultiRowApprovalLockTakesTheCanonicalOrder(t *testing.T) {
	found := 0
	for _, path := range packageSourceFiles(t) {
		for _, stmt := range lockingStatement.FindAllString(codeWithoutComments(t, path), -1) {
			found++
			if singleRowLock.MatchString(stmt) || strings.Contains(stmt, "lockOrder") {
				continue
			}
			t.Errorf("%s locks more than one approval row without the canonical order:\n\n%s\n\n"+
				"Concatenate lockOrder into the statement, or narrow it to one row. Two "+
				"transactions locking a shared set in different orders deadlock, and PostgreSQL "+
				"resolves that by aborting one of them — a 500 on a decision or a re-proposal "+
				"that was otherwise valid.", filepath.Base(path), strings.TrimSpace(stmt))
		}
	}
	if found < lockingStatementFloor {
		t.Fatalf("found %d row-locking statements in package approvals, expected at least %d — the "+
			"scan broke rather than the subject, and a gate reading green off no subjects certifies nothing",
			found, lockingStatementFloor)
	}
}

// codeWithoutComments re-prints one file from its AST, which drops every
// comment and leaves raw string literals — the SQL — verbatim.
func codeWithoutComments(t *testing.T, path string) string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	var buf bytes.Buffer
	if err := printer.Fprint(&buf, fset, file); err != nil {
		t.Fatalf("re-printing %s: %v", path, err)
	}
	return buf.String()
}

// packageSourceFiles lists this package's hand-written Go files. Tests are
// excluded: a test may lock rows in a deliberately wrong order to prove the
// deadlock it is about.
func packageSourceFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		files = append(files, e.Name())
	}
	return files
}
