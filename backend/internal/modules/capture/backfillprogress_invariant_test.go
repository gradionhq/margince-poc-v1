// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The running page's tally lives in the inflight_* columns and is added to the
// committed counters by the status read, so exactly one writer may SET it from
// its parameters and every other writer of an EXISTING run row must ZERO it. A
// new terminal write that forgot the reset would not fail any behavioural test
// — it would quietly report the page's work twice — so the obligation is
// derived from the source.
//
// Derived the way tableownership_test.go derives its own: parse each file,
// reconstruct the effective SQL of every statement (the fragments here are
// const-concatenated, so a literal alone is not the statement), and judge the
// whole statement. A line-window scan over raw text missed a wrapped table
// name, an ON CONFLICT DO UPDATE arm, and a mention of the fragment's name in
// a neighbouring comment.

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

var (
	// A row UPDATE, however the statement wraps or cases it.
	updateRunRe = regexp.MustCompile(`(?is)\bupdate\s+capture_backfill\b`)
	// The upsert spelling of the same thing: the INSERT's conflict arm updates
	// a row that already exists, tally and all.
	insertRunRe = regexp.MustCompile(`(?is)\binsert\s+into\s+capture_backfill\b`)
	doUpdateRe  = regexp.MustCompile(`(?is)\bdo\s+update\s+set\b`)
	// What the statement ASSIGNS, which is the only place a reset can live.
	// Judged over the whole statement instead, a WHERE clause that merely
	// COMPARES the tally to zero scored as a reset that wrote nothing.
	setClauseRe = regexp.MustCompile(`(?is)\bset\b(.*?)(?:\bwhere\b|\breturning\b|$)`)
)

// inflightColumns is the whole transient tally. Every column is checked, not
// just the first: a statement that zeroes inflight_scanned and forgets
// inflight_captured leaves the status read adding a captured count no page
// owns any more, which is the same double-report the reset exists to prevent
// — and it is exactly the kind of half-edit a copy of a neighbouring
// statement produces.
var inflightColumns = []string{
	"inflight_scanned", "inflight_captured", "inflight_skipped",
	"inflight_people", "inflight_organizations",
}

// The two values a statement may assign the tally: the one live writer sets
// every column from its parameters, every other writer of an existing row
// zeroes every column.
const (
	fromParameter = `\$\d+`
	toZero        = `0`
)

// matchesEveryColumn reports whether every inflight column is assigned the
// given value in this statement's SET clause. The value must END its
// assignment — a comma, or the end of the clause — so a literal that merely
// STARTS with the value cannot pass as it (`0.5` is not zero).
func matchesEveryColumn(sql, value string) bool {
	clause := setClauseRe.FindStringSubmatch(sql)
	if clause == nil {
		return false
	}
	for _, column := range inflightColumns {
		assigns := regexp.MustCompile(`(?is)\b` + column + `\s*=\s*` + value + `\s*(?:,|$)`)
		if !assigns.MatchString(clause[1]) {
			return false
		}
	}
	return true
}

func TestEveryBackfillRunWriteSettlesTheInFlightTally(t *testing.T) {
	fset := token.NewFileSet()
	// The whole module subtree: tableownership_test.go lets any package under
	// internal/modules/capture write capture_backfill, so a writer that moved
	// into a subpackage is legal there and must still be checked here.
	var files []*ast.File
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") ||
			strings.HasSuffix(path, "_test.go") || strings.HasSuffix(path, "_gen.go") {
			return err
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		files = append(files, file)
		return nil
	})
	if err != nil {
		t.Fatalf("walking the capture package: %v", err)
	}
	// Consts first, across every file: the shared reset fragment is declared in
	// one file and concatenated into statements written in others.
	// Repeat until the map stops growing: a fragment can be assembled from
	// another fragment declared in a different file, and one pass would leave
	// the outer one unresolved.
	consts := map[string]string{}
	for range files {
		before := len(consts)
		for _, file := range files {
			collectStringConsts(file, consts)
		}
		if len(consts) == before {
			break
		}
	}
	var writers int
	for _, file := range files {
		writers += auditRunWrites(t, fset, file, consts)
	}
	if writers != 1 {
		t.Fatalf("found %d statements that SET the whole in-flight tally from parameters, want exactly 1 (flushBackfillProgress) — two live writers of one transient tally cannot both be right", writers)
	}
}

// auditRunWrites judges every capture_backfill write in one file and returns
// how many of them are the live tally writer.
func auditRunWrites(t *testing.T, fset *token.FileSet, file *ast.File, consts map[string]string) int {
	t.Helper()
	var writers int
	ast.Inspect(file, func(n ast.Node) bool {
		// A statement is a literal or a concatenation of them, never a lone
		// name: an identifier resolves as a PART of a statement, and judging
		// one on its own would re-report the fragment's declaration as a
		// second copy of every statement that uses it.
		var expr ast.Expr
		switch node := n.(type) {
		case *ast.BasicLit:
			expr = node
		case *ast.BinaryExpr:
			expr = node
		default:
			return true
		}
		sql, ok := sqlOf(consts, expr)
		if !ok {
			return true
		}
		// A resolved string expression IS the statement; its own literals are
		// not separate statements, so stop here rather than re-judging each.
		updates := updateRunRe.MatchString(sql) ||
			(insertRunRe.MatchString(sql) && doUpdateRe.MatchString(sql))
		switch {
		case !updates:
		case matchesEveryColumn(sql, fromParameter):
			writers++
		case !matchesEveryColumn(sql, toZero):
			t.Errorf("%s writes an existing capture_backfill row without settling the whole running-page tally (%s) — end it with settleInflightProgress (a page-ending write, which keeps the counterparty yields) or resetInflightProgress (the page commit, which has already folded them in), or the status read counts that page's work twice:\n%s",
				fset.Position(expr.Pos()), strings.Join(inflightColumns, ", "), sql)
		}
		return false
	})
	return writers
}

// collectStringConsts adds one file's string constants to consts, so a
// statement assembled from a shared fragment resolves to what the database
// actually receives. A const may be built FROM another const, so it resolves
// through sqlOf and the caller repeats until the map stops growing — a
// fragment that resolved to nothing would make every statement using it look
// like it settles no tally.
func collectStringConsts(file *ast.File, consts map[string]string) {
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok || len(value.Names) != 1 || len(value.Values) != 1 {
				continue
			}
			name := value.Names[0].Name
			if _, done := consts[name]; done {
				continue
			}
			if text, ok := sqlOf(consts, value.Values[0]); ok {
				consts[name] = text
			}
		}
	}
}

// sqlOf reconstructs a string expression's value. A part it cannot resolve —
// a local variable holding a CASE arm, say — becomes a space: the statement
// keeps its shape, and the fragments this test judges are consts it can read.
// ok is false for anything that is not a string expression at all.
func sqlOf(consts map[string]string, expr ast.Expr) (string, bool) {
	switch node := expr.(type) {
	case *ast.BasicLit:
		if node.Kind != token.STRING {
			return "", false
		}
		text, err := strconv.Unquote(node.Value)
		return text, err == nil
	case *ast.Ident:
		if text, known := consts[node.Name]; known {
			return text, true
		}
		return " ", true
	case *ast.BinaryExpr:
		if node.Op != token.ADD {
			return "", false
		}
		left, leftOK := sqlOf(consts, node.X)
		right, rightOK := sqlOf(consts, node.Y)
		if !leftOK || !rightOK {
			return "", false
		}
		return left + right, true
	default:
		return "", false
	}
}
