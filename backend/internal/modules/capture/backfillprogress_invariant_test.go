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
	// A write that ASSIGNS status is one that ends or moves a page: the run
	// starts running, finishes, errors, or is cancelled. Those are the writes
	// the mirror belongs to. A write that only bumps a counter — the
	// per-creation counterparty count — ends no page and owns no mirror.
	assignsStatusRe = regexp.MustCompile(`(?is)\bstatus\s*=`)
)

// inflightColumns is the whole transient tally — the MESSAGE counts only; a
// counterparty creation is counted straight into the committed columns and has
// no mirror. Every column is checked, not just the first: a statement that
// zeroes inflight_scanned and forgets inflight_captured leaves the status read
// adding a captured count no page owns any more, which is the same
// double-report the reset exists to prevent, and it is exactly the half-edit a
// copy of a neighbouring statement produces.
var inflightColumns = []string{
	"inflight_scanned", "inflight_captured", "inflight_skipped",
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
	// Repeat until the map stops growing — a real fixed point, with no
	// iteration budget. A fragment can be assembled from another fragment, and
	// each pass resolves one more link of such a chain, so the passes needed
	// track the chain's DEPTH. Bounding the loop by the file count instead
	// happened to be enough only because this package has more files than any
	// chain has links: one file holding a two-link chain would have exhausted
	// the budget early and left the outer fragment unresolved, which reads as a
	// correct write clearing nothing. Termination is not at risk: consts only
	// grows, and it is bounded by the number of const declarations in the tree.
	consts := map[string]string{}
	for {
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
		sql, ok := sqlOf(consts, expr, blankIdents)
		if !ok {
			return true
		}
		// A resolved string expression IS the statement; its own literals are
		// not separate statements, so stop here rather than re-judging each.
		updates := updateRunRe.MatchString(sql) ||
			(insertRunRe.MatchString(sql) && doUpdateRe.MatchString(sql))
		clause := setClauseRe.FindStringSubmatch(sql)
		endsAPage := clause != nil && assignsStatusRe.MatchString(clause[1])
		switch {
		case !updates || !endsAPage:
		case matchesEveryColumn(sql, fromParameter):
			writers++
		case !matchesEveryColumn(sql, toZero):
			t.Errorf("%s writes an existing capture_backfill row without settling the whole running-page tally (%s) — a write that assigns status ends or moves a page, so it must end with resetInflightProgress, or the status read keeps counting that page's messages:\n%s",
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
			// STRICT: a const assembled from a const not yet collected is left
			// for the next pass, not cached with a placeholder standing in for
			// its missing half. Caching it would freeze the placeholder — the
			// loop that exists to resolve exactly this case could never correct
			// it, and every statement built from that fragment would be judged
			// as if the fragment said nothing.
			if text, ok := sqlOf(consts, value.Values[0], strictIdents); ok {
				consts[name] = text
			}
		}
	}
}

// identMode says what an unresolvable identifier means to the caller.
type identMode bool

const (
	// strictIdents refuses the whole expression — used while collecting consts,
	// where an unknown name means "not resolvable YET".
	strictIdents identMode = true
	// blankIdents substitutes a space — used while judging a statement, where an
	// unknown name is a local variable holding a CASE arm and it is the
	// statement's shape that matters.
	blankIdents identMode = false
)

// sqlOf reconstructs a string expression's value. ok is false for anything that
// is not a string expression at all, and — under strictIdents — for anything
// carrying a name it cannot resolve.
func sqlOf(consts map[string]string, expr ast.Expr, idents identMode) (string, bool) {
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
		if idents == strictIdents {
			return "", false
		}
		return " ", true
	case *ast.BinaryExpr:
		if node.Op != token.ADD {
			return "", false
		}
		left, leftOK := sqlOf(consts, node.X, idents)
		right, rightOK := sqlOf(consts, node.Y, idents)
		if !leftOK || !rightOK {
			return "", false
		}
		return left + right, true
	default:
		return "", false
	}
}
