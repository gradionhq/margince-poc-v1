// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package approvals

// A lock order is only a rule if every locker follows it.
//
// The deadlock this guards is not a race one reviewer can see in one file: it
// needs two statements, usually in two files, that lock a shared set of
// `approval` rows in different orders. Each reads correctly alone, which is why
// the obligation is derived from the package's own SQL rather than kept as a
// list somebody remembers to extend.
//
// THE SUBJECT IS ONE SQL EXPRESSION AT A TIME, resolved from the syntax tree.
// A first draft scanned the flattened file text for `FOR UPDATE` and walked
// backwards to a verb, and review demonstrated the hole: the span it matched
// began at an unrelated statement several functions earlier, whose
// `WHERE id = $1` then excused everything up to the next `FOR UPDATE` — so
// deleting the order from the very statement this gate exists to protect
// produced no finding at all. Folding each SQL expression separately is what
// makes each statement judged as itself.
//
// It also judges statements that carry NO lock clause. A multi-row UPDATE or
// DELETE takes row locks in scan order without saying so, which is exactly the
// defect the issue names — a gate that only looked for FOR UPDATE would read
// green over the shape it was written for.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var (
	// touchesApproval finds the statements this rule is about at all: a read of
	// the approval table, or a write to it.
	touchesApproval = regexp.MustCompile(`(?is)\b(?:FROM|UPDATE|INTO)\s+approval\b`)

	// takesRowLocks recognises what actually locks rows. Every lock STRENGTH,
	// not just FOR UPDATE — a weaker lock still queues against a stronger one
	// and can still close a cycle — plus the bare UPDATE and DELETE, which lock
	// every row they match without a clause to announce it.
	takesRowLocks = regexp.MustCompile(`(?is)\bFOR\s+(?:UPDATE|NO\s+KEY\s+UPDATE|SHARE|KEY\s+SHARE)\b|^\s*(?:UPDATE\s+approval|DELETE\s+FROM\s+approval)\b`)

	// oneRowOnly recognises the shapes that can only ever touch a single row.
	// One lock has no order to get wrong: an order exists between rows, and
	// there is no second row.
	oneRowOnly = regexp.MustCompile(`(?is)WHERE\s+id\s*=\s*\$\d\b|LIMIT 1\b`)

	// alreadyHeld recognises a write addressed to an explicit id set. It is
	// exempt for a reason rather than by oversight: the caller resolved that set
	// with a canonically ordered locking read moments earlier, so the write
	// acquires NO new lock and there is no order left for it to take. A caller
	// that reached this shape any other way would be re-introducing the bug, so
	// the exemption is narrow on purpose — an id set, nothing else.
	alreadyHeld = regexp.MustCompile(`(?is)WHERE\s+id\s*=\s*ANY\(`)
)

// The floor that keeps this from certifying nothing: the package really does
// lock and write approval rows in several places, and a walk that suddenly
// finds none has broken rather than been satisfied.
const approvalStatementFloor = 8

func TestEveryMultiRowApprovalLockTakesTheCanonicalOrder(t *testing.T) {
	found := 0
	for _, path := range packageSourceFiles(t) {
		for _, sql := range sqlExpressions(t, path) {
			if !touchesApproval.MatchString(sql) {
				continue
			}
			found++
			if !takesRowLocks.MatchString(sql) {
				continue
			}
			if oneRowOnly.MatchString(sql) || alreadyHeld.MatchString(sql) || strings.Contains(sql, canonicalOrder) {
				continue
			}
			t.Errorf("%s locks or writes more than one approval row without the canonical order:\n\n%s\n\n"+
				"Concatenate lockOrder into the statement, or narrow it to one row. Two transactions "+
				"locking a shared set in different orders deadlock, and PostgreSQL resolves that by "+
				"aborting one of them — a 500 on a decision or a re-proposal that was otherwise valid.",
				filepath.Base(path), strings.TrimSpace(sql))
		}
	}
	if found < approvalStatementFloor {
		t.Fatalf("resolved only %d SQL expressions touching `approval`, expected at least %d — the "+
			"folding broke rather than the subject, and a gate reading green off no subjects "+
			"certifies nothing", found, approvalStatementFloor)
	}
}

// canonicalOrder is the text lockOrder carries. Spelled out rather than
// referenced so this gate compares against the ORDER ITSELF: a constant renamed
// to lockOrder while carrying some other order would satisfy a name check and
// nothing else.
const canonicalOrder = "ORDER BY created_at, id"

// sqlExpressions folds every string-valued expression in one file into its
// text, one expression at a time.
//
// The folding resolves identifiers against the file's own constants, because
// the order arrives as `... + lockOrder + ...` and the column list as
// `SELECT ` + columns — an unresolved identifier would leave the gate reading a
// statement with its order removed and finding nothing wrong with it.
func sqlExpressions(t *testing.T, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	consts := stringConstants(file)
	var out []string
	ast.Inspect(file, func(n ast.Node) bool {
		expr, ok := n.(ast.Expr)
		if !ok {
			return true
		}
		text, folded := foldString(expr, consts)
		if !folded || !strings.Contains(text, " ") {
			return true
		}
		out = append(out, text)
		// Do not descend: the halves of a folded expression are the same
		// statement, and counting them again would let a fragment carrying an
		// innocent `WHERE id = $1` be judged as a statement of its own.
		return false
	})
	return out
}

// stringConstants indexes the file's string constants — package-level and
// function-local alike, since declinedProbeSQL builds its statement from two
// locals.
func stringConstants(file *ast.File) map[string]string {
	consts := map[string]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		decl, ok := n.(*ast.GenDecl)
		if !ok || decl.Tok != token.CONST {
			return true
		}
		for _, spec := range decl.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) != len(vs.Values) {
				continue
			}
			for i, name := range vs.Names {
				if text, folded := foldString(vs.Values[i], consts); folded {
					consts[name.Name] = text
				}
			}
		}
		return true
	})
	return consts
}

// foldString resolves one expression to its string value, following literals,
// known constants and `+` concatenation. Anything else — a function call, a
// variable — folds to nothing, and the expression is not judged: a statement
// this gate cannot read whole is one it must not pass silently either, which is
// what the subject floor above is for.
func foldString(expr ast.Expr, consts map[string]string) (string, bool) {
	switch e := expr.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		text, err := strconv.Unquote(e.Value)
		return text, err == nil
	case *ast.Ident:
		text, ok := consts[e.Name]
		return text, ok
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return "", false
		}
		left, okL := foldString(e.X, consts)
		right, okR := foldString(e.Y, consts)
		return left + right, okL && okR
	case *ast.ParenExpr:
		return foldString(e.X, consts)
	default:
		return "", false
	}
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
