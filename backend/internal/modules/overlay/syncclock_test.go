// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

// The sweep schedule is written by ONE clock, derived from this package's
// source rather than remembered. DueOverlayConnections compares next_sweep_at
// against now() INSIDE Postgres (connectionreads.go), so every statement that
// writes the column has to take its value from that same now(); a deadline
// bound from Go makes the comparison cross-clock, and two clocks are only ever
// coincidentally equal.
//
// This is a gate rather than a paragraph because a comment cannot fail. The
// same rationale is now written twice in the tree — capture's MarkQueued
// carries it for next_attempt_at — and on both occasions nothing but habit was
// keeping it true. It reads the source because the defect is invisible at
// runtime on any machine whose two clocks agree, which is every CI runner and
// every developer laptop: a test that exercised the store could not fail
// against the bug this gate describes.
//
// Scope is this package because overlay_sync_state is owned here, pinned by
// tableownership_test.go — no other package may write the column at all.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// scheduleColumn is the column under the rule. Its two write spellings are an
// assignment (`SET`/`DO UPDATE SET`) and a position in an INSERT column list,
// and the gate reads both.
const scheduleColumn = "next_sweep_at"

// databaseClock is the only sanctioned start point for a scheduling value.
// A delay is added to it (`now() + make_interval(secs => $1)`), which is why
// this is a prefix test and not equality.
const databaseClock = "now()"

// TestEverySweepScheduleAssignmentTakesTheDatabaseClock covers the assignment
// form: `next_sweep_at = <expr>`. The expression has to start at the database
// clock — `$1` (a Go timestamp) and `EXCLUDED.next_sweep_at` (whatever the
// INSERT proposed, which may itself have been bound) both fail.
func TestEverySweepScheduleAssignmentTakesTheDatabaseClock(t *testing.T) {
	subjects := 0
	for _, file := range packageSourceFiles(t) {
		for _, rhs := range assignmentsTo(sourceOf(t, file), scheduleColumn) {
			subjects++
			if !strings.HasPrefix(rhs, databaseClock) {
				t.Errorf("%s: `%s = %s` schedules from something other than the database clock — "+
					"the due-scan compares this column against Postgres now(), so the value must start at now()",
					filepath.Base(file), scheduleColumn, rhs)
			}
		}
	}
	requireSubjects(t, subjects)
}

// TestEverySweepScheduleInsertTakesTheDatabaseClock covers the other form: the
// column named in an INSERT column list, whose value is positional. A fresh
// row is due immediately, so the VALUES item is the bare clock — a backoff on
// a first insert would be pacing a sweep that has never run.
func TestEverySweepScheduleInsertTakesTheDatabaseClock(t *testing.T) {
	subjects := 0
	for _, file := range packageSourceFiles(t) {
		for _, value := range insertedValuesFor(sourceOf(t, file), scheduleColumn) {
			subjects++
			if value != databaseClock {
				t.Errorf("%s: INSERT writes %s as `%s`, want the database clock `%s`",
					filepath.Base(file), scheduleColumn, value, databaseClock)
			}
		}
	}
	requireSubjects(t, subjects)
}

// requireSubjects fails a gate that examined nothing. Both checks here are
// keyed on a column NAME, so renaming the column — or moving the writes behind
// a query builder — would leave them iterating an empty set and reporting
// success. An absence-assertion passes for free; this is what makes these two
// report on something.
func requireSubjects(t *testing.T, found int) {
	t.Helper()
	if found == 0 {
		t.Fatalf("no %s write site found in this package — the gate examined nothing, "+
			"which is not the same as finding nothing wrong. If the column was renamed or the "+
			"writes moved, retarget scheduleColumn; the database-clock rule still holds.", scheduleColumn)
	}
}

// packageSourceFiles lists this package's hand-written Go, test files
// excluded. A fixture may legitimately stamp a schedule into the past or the
// future to put the store in a state — that is the test describing a world,
// not production choosing a clock.
func packageSourceFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}
	var files []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		files = append(files, name)
	}
	if len(files) == 0 {
		t.Fatal("no package source files found — the gate would pass over an empty set")
	}
	return files
}

func sourceOf(t *testing.T, file string) string {
	t.Helper()
	text, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("reading %s: %v", file, err)
	}
	return string(text)
}

// assignmentsTo returns the right-hand side of every `column = ...` in text,
// read to the end of its line. The repo writes one assignment per line inside
// its SQL literals, so the line IS the expression; a trailing comma from the
// SET list is not part of it.
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
// `INSERT INTO ... (cols) VALUES (vals)` in text. It matches by POSITION,
// which is what Postgres does; a gate that merely looked for the column name
// near a now() would pass a statement whose columns and values had drifted
// out of step.
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

// parenGroup returns the contents of the first parenthesised group in text
// and whatever follows it, matching nested parens so a group containing a
// function call is not cut short.
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
// parentheses or quotes. The workspace-GUC expression every INSERT here opens
// with is ONE item that contains two commas of its own; a gate that split it
// into three would misalign every position after it, and then report the
// misalignment as a clock violation.
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
