// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package gatekit

// RequireDatabaseClock is the gate two modules rely on to keep a scheduling
// column on the database's clock, and a gate is only worth the cases it can
// FAIL. Each case here builds a one-file package in a temp dir and asserts on
// what the gate reported, not on whether it passed.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// packageWith writes source as the only Go file of a throwaway package and
// returns its directory.
func packageWith(t *testing.T, source string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "store.go"), []byte(source), 0o600); err != nil {
		t.Fatalf("writing the fixture package: %v", err)
	}
	return dir
}

// reportOn runs the gate over a fixture package and returns everything it said.
func reportOn(t *testing.T, source, column string) string {
	t.Helper()
	rec := &recorder{TB: t}
	RequireDatabaseClock(rec, packageWith(t, source), column)
	return rec.joined()
}

const compliantStore = "package store\n\nconst q = `\n\tINSERT INTO sync_state (id, next_sync_at, updated_at)\n\tVALUES ($1, now() + make_interval(secs => $2), now())\n\tON CONFLICT (id) DO UPDATE SET\n\t  next_sync_at = now() + make_interval(secs => $2),\n\t  updated_at = now()`\n"

func TestAScheduleWrittenFromTheDatabaseClockIsAccepted(t *testing.T) {
	if got := reportOn(t, compliantStore, "next_sync_at"); got != "" {
		t.Errorf("a compliant package reported: %s", got)
	}
}

func TestAnAssignmentBoundFromTheAppClockIsReported(t *testing.T) {
	const source = "package store\n\nconst q = `\n\tUPDATE sync_state SET next_sync_at = $2\n\tWHERE id = $1`\n"
	got := reportOn(t, source, "next_sync_at")
	if !strings.Contains(got, "`next_sync_at = $2`") {
		t.Errorf("a Go-bound assignment was not reported: %q", got)
	}
}

// The assignment's expression is read to the end of its LINE, which is what the
// repo's SQL literals put one per line. A statement that also puts its WHERE
// there is still judged on the same rule — the reported expression just carries
// the rest of the line with it.
func TestASingleLineStatementIsStillJudged(t *testing.T) {
	const source = "package store\n\nconst q = `UPDATE sync_state SET next_sync_at = $2 WHERE id = $1`\n"
	got := reportOn(t, source, "next_sync_at")
	if !strings.Contains(got, "next_sync_at = $2") {
		t.Errorf("a one-line Go-bound assignment escaped the gate: %q", got)
	}

	const compliant = "package store\n\nconst q = `UPDATE sync_state SET next_sync_at = now() WHERE id = $1`\n"
	if got := reportOn(t, compliant, "next_sync_at"); got != "" {
		t.Errorf("a one-line compliant assignment was reported: %s", got)
	}
}

func TestAnInsertedValueBoundFromTheAppClockIsReported(t *testing.T) {
	const source = "package store\n\nconst q = `INSERT INTO sync_state (id, next_sync_at) VALUES ($1, $2)`\n"
	got := reportOn(t, source, "next_sync_at")
	if !strings.Contains(got, "INSERT writes next_sync_at as `$2`") {
		t.Errorf("a Go-bound INSERT value was not reported: %q", got)
	}
}

// The INSERT check matches by POSITION, so a statement whose columns and values
// have drifted apart must be judged on the value Postgres would actually store
// — here the bound $2, two positions along from the column's own place.
func TestAnInsertIsJudgedOnThePositionPostgresWouldUse(t *testing.T) {
	const source = "package store\n\nconst q = `\n\tINSERT INTO sync_state (workspace_id, id, next_sync_at)\n\tVALUES (NULLIF(current_setting('app.workspace_id',true),'')::uuid, $1, $2)`\n"
	got := reportOn(t, source, "next_sync_at")
	if !strings.Contains(got, "as `$2`") {
		t.Errorf("the GUC expression's own commas misaligned the position: %q", got)
	}
}

// A comment is not a write. This is the false positive the gate shipped with
// when it read raw source: capture's registry.go explains the schedule in prose
// ("next_sync_at = success + interval"), which reads exactly like an assignment
// and is not one.
func TestProseDescribingTheScheduleIsNotAWrite(t *testing.T) {
	const source = "package store\n\n// The healthy connection (next_sync_at = success + interval);\n// nothing here writes it.\nconst q = `INSERT INTO sync_state (id, next_sync_at) VALUES ($1, now())`\n"
	got := reportOn(t, source, "next_sync_at")
	if got != "" {
		t.Errorf("a comment was judged as a write: %s", got)
	}
}

// The two checks are keyed on a column NAME, so a rename would leave them
// iterating an empty set — and an absence-assertion passes for free. A gate
// that examined nothing must say so rather than report success.
func TestAGateThatFoundNoWriteSiteSaysSo(t *testing.T) {
	got := reportOn(t, compliantStore, "next_attempt_at")
	if !strings.Contains(got, "the gate examined nothing") {
		t.Errorf("a gate with no subjects passed silently: %q", got)
	}
}

func TestAPackageWithNoSourceIsReportedRatherThanPassed(t *testing.T) {
	rec := &recorder{TB: t}
	RequireDatabaseClock(rec, t.TempDir(), "next_sync_at")
	if !strings.Contains(rec.joined(), "would pass over an empty set") {
		t.Errorf("an empty package passed: %q", rec.joined())
	}
}

// Test files are excluded on purpose: a fixture may stamp a schedule into the
// past or the future to put a store in a state, which is a test describing a
// world rather than production choosing a clock.
func TestAFixtureInATestFileIsNotJudged(t *testing.T) {
	dir := packageWith(t, compliantStore)
	const fixture = "package store\n\nconst seed = `UPDATE sync_state SET next_sync_at = $1`\n"
	if err := os.WriteFile(filepath.Join(dir, "store_test.go"), []byte(fixture), 0o600); err != nil {
		t.Fatalf("writing the fixture test file: %v", err)
	}
	rec := &recorder{TB: t}
	RequireDatabaseClock(rec, dir, "next_sync_at")
	if got := rec.joined(); got != "" {
		t.Errorf("a test fixture was judged as production: %s", got)
	}
}
