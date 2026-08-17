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
	"slices"
	"strings"
	"testing"
)

// databaseClock is the only sanctioned start point for a scheduling value. A
// delay is added to it (`now() + make_interval(secs => $1)`), which is why the
// checks are prefix tests and not equality.
const databaseClock = "now()"

// unscheduled is the other value a scheduling column legitimately holds: no
// deadline at all. It involves no clock, so it is outside the rule rather than
// an exception to it.
const unscheduled = "NULL"

// DatabaseClock is one package's obligation for one scheduling column: every
// write of Column, in the Go sources under Dir, takes its value from the
// database's clock.
type DatabaseClock struct {
	// Dir is the package directory to read, usually "." — the package whose
	// tableownership entry names the table this column belongs to.
	Dir string
	// Column is the scheduling column under the rule.
	Column string
	// Exempt ratifies FILES whose writes of Column are legitimately not derived
	// from any clock of ours — an expiry the granting human chose, a deadline a
	// provider returned. Keyed by base name, each carrying what the exception
	// costs; a waiver matching nothing is reported as stale.
	Exempt *Waivers[string]
}

// Require reports every write of the column whose value does not come from the
// database clock, and fails a run that found no write at all.
//
// The column has two write spellings and both are read: an assignment
// (`SET` / `DO UPDATE SET`), and a position in an INSERT column list.
func (c DatabaseClock) Require(t testing.TB) {
	t.Helper()
	subjects := 0
	for _, file := range packageSourceFiles(t, c.Dir) {
		text := sqlOf(t, file)
		name := filepath.Base(file)

		// The assignment form: `column = <expr>`. The expression has to START at
		// the database clock — `$1` (a Go timestamp) and `EXCLUDED.<column>`
		// (whatever the INSERT proposed, which may itself have been bound) both
		// fail.
		for _, rhs := range assignmentsTo(text, c.Column) {
			subjects++
			if !c.accepts(t, name, rhs) {
				t.Errorf("%s: `%s = %s` schedules from something other than the database clock — "+
					"the due-scan compares this column against Postgres now(), so the value must start at now()",
					name, c.Column, rhs)
			}
		}

		// The INSERT form, whose value is positional. A fresh row's schedule is
		// either the bare clock or the clock plus a delay; a bound timestamp is
		// the same cross-clock write one statement over.
		for _, value := range insertedValuesFor(text, c.Column) {
			subjects++
			if !c.accepts(t, name, value) {
				t.Errorf("%s: INSERT writes %s as `%s`, want a value starting at the database clock `%s`",
					name, c.Column, value, databaseClock)
			}
		}
	}
	requireSubjects(t, c.Dir, c.Column, subjects)
}

// accepts reports whether one written value satisfies the rule, or its file is
// ratified as writing a deadline that is nobody's clock of ours.
func (c DatabaseClock) accepts(t testing.TB, file, value string) bool {
	t.Helper()
	if databaseClocked(value) {
		return true
	}
	return c.Exempt.Waived(t, file)
}

// clampFunctions are the SQL functions this rule sees through. Each PICKS one
// of its arguments rather than computing a value of its own, so a clamp is
// database-clocked exactly when everything it may pick is —
// `least(now() + $1::interval, expires_at)` bounds an idle window by an
// absolute one, and both terms are the database's.
var clampFunctions = []string{"least", "greatest", "coalesce"}

// databaseClocked reports whether a written value can only have come from the
// database's own clock or from what the row already held.
//
// Four shapes qualify. `now()` and anything built on it is the rule itself. A
// NULL writes no deadline at all, so no clock is involved. A bare column
// forwards a value the database already stores — the sibling column's own
// obligation, not this write's. And a clamp qualifies when every argument does.
func databaseClocked(value string) bool {
	v := strings.TrimSpace(value)
	if strings.HasPrefix(v, databaseClock) || v == unscheduled {
		return true
	}
	if args, ok := clampArguments(v); ok {
		for _, arg := range args {
			if !databaseClocked(arg) {
				return false
			}
		}
		return true
	}
	return storedColumn(v)
}

// clampArguments returns the arguments of a clamp call, or false for anything
// else — including a call to some other function, whose result this rule cannot
// reason about.
func clampArguments(value string) ([]string, bool) {
	name, rest, ok := strings.Cut(value, "(")
	if !ok || !slices.Contains(clampFunctions, strings.ToLower(strings.TrimSpace(name))) {
		return nil, false
	}
	group, tail, ok := parenGroup("(" + rest)
	if !ok || strings.TrimSpace(tail) != "" {
		return nil, false
	}
	return splitTopLevel(group), true
}

// storedColumn reports whether value is a plain column reference — a value the
// row already holds, forwarded.
//
// `EXCLUDED.x` is deliberately NOT one. It forwards whatever the INSERT
// proposed, which may itself have been bound from the app process, so accepting
// it would let any conflict clause launder a cross-clock write.
func storedColumn(value string) bool {
	if value == "" || strings.HasPrefix(strings.ToLower(value), "excluded.") {
		return false
	}
	for i := range len(value) {
		c := value[i]
		identifier := c == '_' || c == '.' ||
			(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if !identifier {
			return false
		}
	}
	return true
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

// assignmentsTo returns the right-hand side of every `column = ...` in text.
// The expression ends at the SET list's next top-level comma — not at the end
// of the line, because a line may carry several assignments (`next_attempt_at =
// NULL, claimed_until = NULL`) and reading to its end would judge the gated
// column on its neighbours' text. Commas nested in parentheses stay part of the
// expression, so `least(now() + $1::interval, expires_at)` is read whole.
//
// The name is matched on a WORD boundary, not as a substring. Deadline columns
// share suffixes — `idle_expires_at` ends in `expires_at`, `metadata_expires_at`
// and `watch_expires_at` likewise — so a substring match reports a sibling
// column's write under the gated column's name, and the reader who goes to fix
// it finds a line that was already correct.
func assignmentsTo(text, column string) []string {
	var found []string
	for _, line := range strings.Split(text, "\n") {
		before, rhs, ok := strings.Cut(line, column+" = ")
		if !ok || endsInIdentifier(before) {
			continue
		}
		found = append(found, endOfExpression(splitTopLevel(rhs)[0]))
	}
	return found
}

// clauseKeywords end a SET item as surely as a comma does. The tree writes its
// SQL one clause per line, so these matter only for a statement written on one
// — but there the difference is between reading an expression and reading an
// expression plus the rest of the statement, and the second is reported as a
// finding against a line that was already correct.
var clauseKeywords = []string{" WHERE ", " RETURNING ", " FROM ", " ON CONFLICT "}

// endOfExpression trims a written value at the first clause keyword after it.
func endOfExpression(value string) string {
	for _, keyword := range clauseKeywords {
		if cut, _, found := strings.Cut(value, keyword); found {
			value = cut
		}
	}
	return strings.TrimSpace(value)
}

// endsInIdentifier reports whether text ends in a character SQL would read as
// part of the name that follows it — which is what makes a match a suffix of a
// longer column rather than the column itself.
func endsInIdentifier(text string) bool {
	if text == "" {
		return false
	}
	c := text[len(text)-1]
	return c == '_' || c == '.' ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
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
