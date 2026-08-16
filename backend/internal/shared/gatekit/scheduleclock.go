// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package gatekit

// A scheduling column is written by ONE clock: the database's.
//
// A due-scan compares its column against now() INSIDE Postgres, so every
// statement that writes the column has to take its value from that same now().
// A deadline bound from Go makes the comparison cross-clock, and two clocks are
// only ever coincidentally equal — an app process running AHEAD pushes work
// into the future and starves it, one running BEHIND shortens every delay and
// can defeat a backoff outright.
//
// The rule needs a gate rather than a paragraph because the defect is invisible
// at runtime on any machine whose two clocks agree, which is every CI runner and
// every developer laptop: no test that exercises the store can fail against it.
// It needs a SHARED gate because the tree has now reached for the same rationale
// in four places, and the two occasions it was written as a comment were both
// occasions where habit was the only thing keeping it true.
//
// The obligation belongs to whichever package owns the table (tableownership
// pins that), so each such package instantiates this on its own column: the
// write sites are derived from source, and only the column is named.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// databaseClock is the only sanctioned start point for a scheduling value. A
// delay is added to it (`now() + make_interval(secs => $1)`), which is why the
// assignment check is a prefix test and not equality.
const databaseClock = "now()"

// RequireDatabaseClock reports every write of column in dir's package sources
// whose value does not come from the database clock, and fails a run that found
// no write at all.
//
// The column has two write spellings and both are read: an assignment
// (`SET` / `DO UPDATE SET`), and a position in an INSERT column list.
func RequireDatabaseClock(t testing.TB, dir, column string) {
	t.Helper()
	subjects := 0
	for _, file := range packageSourceFiles(t, dir) {
		text := sqlOf(t, file)
		name := filepath.Base(file)

		// The assignment form: `column = <expr>`. The expression has to START at
		// the database clock — `$1` (a Go timestamp) and `EXCLUDED.<column>`
		// (whatever the INSERT proposed, which may itself have been bound) both
		// fail.
		for _, rhs := range assignmentsTo(text, column) {
			subjects++
			if !strings.HasPrefix(rhs, databaseClock) {
				t.Errorf("%s: `%s = %s` schedules from something other than the database clock — "+
					"the due-scan compares this column against Postgres now(), so the value must start at now()",
					name, column, rhs)
			}
		}

		// The INSERT form, whose value is positional. A fresh row's schedule is
		// either the bare clock or the clock plus a delay; a bound timestamp is
		// the same cross-clock write one statement over.
		for _, value := range insertedValuesFor(text, column) {
			subjects++
			if !strings.HasPrefix(value, databaseClock) {
				t.Errorf("%s: INSERT writes %s as `%s`, want a value starting at the database clock `%s`",
					name, column, value, databaseClock)
			}
		}
	}
	requireSubjects(t, dir, column, subjects)
}

// requireSubjects fails a gate that examined nothing. The checks are keyed on a
// column NAME, so renaming the column — or moving the writes behind a query
// builder — would leave them iterating an empty set and reporting success. An
// absence-assertion passes for free; this is what makes the gate report on
// something.
func requireSubjects(t testing.TB, dir, column string, found int) {
	t.Helper()
	if found == 0 {
		t.Errorf("no %s write site found in %s — the gate examined nothing, which is not the same as "+
			"finding nothing wrong. If the column was renamed or the writes moved, retarget it; "+
			"the database-clock rule still holds.", column, dir)
	}
}

// packageSourceFiles lists dir's hand-written Go, test files excluded. A fixture
// may legitimately stamp a schedule into the past or the future to put a store
// in a state — that is a test describing a world, not production choosing a
// clock.
func packageSourceFiles(t testing.TB, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Errorf("reading the package directory %s: %v", dir, err)
		return nil
	}
	var files []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		files = append(files, filepath.Join(dir, name))
	}
	if len(files) == 0 {
		t.Errorf("no package source files under %s — the gate would pass over an empty set", dir)
	}
	return files
}

// sqlOf returns file's string literals, newline-separated — the statements the
// package actually sends, and nothing else. Reading the raw text instead would
// judge prose: a comment describing the schedule ("next_sync_at = success +
// interval") is not a write, and a gate that reports one teaches its readers to
// reword comments to stay green.
func sqlOf(t testing.TB, file string) string {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Errorf("parsing %s: %v", file, err)
		return ""
	}
	var literals []string
	ast.Inspect(parsed, func(n ast.Node) bool {
		if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			literals = append(literals, lit.Value)
		}
		return true
	})
	return strings.Join(literals, "\n")
}

// assignmentsTo returns the right-hand side of every `column = ...` in text,
// read to the end of its line. The repo writes one assignment per line inside
// its SQL literals, so the line IS the expression; a trailing comma from the SET
// list is not part of it.
func assignmentsTo(text, column string) []string {
	var found []string
	for _, line := range strings.Split(text, "\n") {
		_, rhs, ok := strings.Cut(line, column+" = ")
		if !ok {
			continue
		}
		found = append(found, strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rhs), ",")))
	}
	return found
}

// insertedValuesFor returns the VALUES item matching column for every
// `INSERT INTO ... (cols) VALUES (vals)` in text. It matches by POSITION, which
// is what Postgres does; a gate that merely looked for the column name near a
// now() would pass a statement whose columns and values had drifted out of step.
func insertedValuesFor(text, column string) []string {
	var found []string
	rest := text
	for {
		_, after, ok := strings.Cut(rest, "INSERT INTO ")
		if !ok {
			return found
		}
		rest = after
		cols, tail, ok := parenGroup(rest)
		if !ok {
			continue
		}
		_, afterValues, ok := strings.Cut(tail, "VALUES")
		if !ok {
			continue
		}
		vals, _, ok := parenGroup(afterValues)
		if !ok {
			continue
		}
		names, items := splitTopLevel(cols), splitTopLevel(vals)
		for i, name := range names {
			if name == column && i < len(items) {
				found = append(found, items[i])
			}
		}
	}
}

// parenGroup returns the contents of the first parenthesised group in text and
// whatever follows it, matching nested parens so a group containing a function
// call is not cut short.
func parenGroup(text string) (group, tail string, ok bool) {
	start := strings.Index(text, "(")
	if start < 0 {
		return "", "", false
	}
	depth := 0
	for i := start; i < len(text); i++ {
		switch text[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return text[start+1 : i], text[i+1:], true
			}
		}
	}
	return "", "", false
}

// splitTopLevel splits a comma-separated SQL list, ignoring commas nested in
// parentheses or quotes. A workspace-GUC expression an INSERT opens with is ONE
// item containing two commas of its own; a gate that split it into three would
// misalign every position after it, and then report the misalignment as a clock
// violation.
func splitTopLevel(list string) []string {
	var items []string
	depth, quoted, start := 0, false, 0
	for i := 0; i < len(list); i++ {
		switch c := list[i]; {
		case c == '\'':
			quoted = !quoted
		case quoted:
		case c == '(':
			depth++
		case c == ')':
			depth--
		case c == ',' && depth == 0:
			items = append(items, strings.TrimSpace(list[start:i]))
			start = i + 1
		}
	}
	return append(items, strings.TrimSpace(list[start:]))
}
