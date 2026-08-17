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
	DatabaseClock{Dir: packageWith(t, source), Column: column}.Require(rec)
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

// A statement written on ONE line is judged on the same rule as the tree's
// usual clause-per-line SQL: the expression ends at the SET list's comma or at
// the clause that follows it, so the WHERE is not read as part of the value.
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
	DatabaseClock{Dir: t.TempDir(), Column: "next_sync_at"}.Require(rec)
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
	DatabaseClock{Dir: dir, Column: "next_sync_at"}.Require(rec)
	if got := rec.joined(); got != "" {
		t.Errorf("a test fixture was judged as production: %s", got)
	}
}

// Deadline columns share suffixes: `idle_expires_at` ends in `expires_at`, and
// so do `metadata_expires_at` and `watch_expires_at`. A substring match reports
// a sibling's write under the gated column's name, and sends its reader to a
// line that was already correct.
func TestASiblingColumnEndingInTheGatedNameIsNotItsWrite(t *testing.T) {
	const source = "package store\n\nconst q = `\n\tUPDATE session SET idle_expires_at = $2\n\tWHERE id = $1`\n"
	rec := &recorder{TB: t}
	DatabaseClock{Dir: packageWith(t, source), Column: "expires_at"}.Require(rec)
	got := rec.joined()
	if strings.Contains(got, "idle_expires_at = $2") {
		t.Errorf("a sibling column was judged as the gated one: %q", got)
	}
	if !strings.Contains(got, "the gate examined nothing") {
		t.Errorf("the gated column has no write here, which the gate must say: %q", got)
	}
}

// The gated column itself is still found when a sibling shares its file.
func TestTheGatedColumnIsStillFoundBesideItsSibling(t *testing.T) {
	const source = "package store\n\nconst q = `\n\tINSERT INTO session (idle_expires_at, expires_at)\n\tVALUES (now() + $1::interval, $2)`\n"
	got := reportOn(t, source, "expires_at")
	if !strings.Contains(got, "as `$2`") {
		t.Errorf("the gated column's own write was missed: %q", got)
	}
	if strings.Contains(got, "idle") {
		t.Errorf("the sibling was reported too: %q", got)
	}
}

// NULL is not a deadline written from the wrong clock; it is no deadline at
// all. Clearing a schedule involves no clock, so it sits outside the rule.
func TestClearingAScheduleIsNotAClockWrite(t *testing.T) {
	const source = "package store\n\nconst q = `\n\tUPDATE sync_state SET next_sync_at = NULL, updated_at = now()\n\tWHERE id = $1`\n"
	if got := reportOn(t, source, "next_sync_at"); got != "" {
		t.Errorf("clearing the schedule was reported: %s", got)
	}
}

// A file may write a deadline that is nobody's clock of ours — an expiry the
// granting human chose, a window a provider returned. That is ratified per file
// with the reason it costs, and gatekit holds the reason to its usual floor.
func TestARatifiedFileMayWriteADeadlineWeDidNotCompute(t *testing.T) {
	const source = "package store\n\nconst q = `\n\tINSERT INTO record_grant (subject_id, expires_at)\n\tVALUES ($1, $2)`\n"
	rec := &recorder{TB: t}
	DatabaseClock{
		Dir:    packageWith(t, source),
		Column: "expires_at",
		Exempt: Waive(map[string]string{
			"store.go": "the expiry is the granting human's own choice, an absolute instant this system did not compute and must not round to its own clock",
		}),
	}.Require(rec)
	if got := rec.joined(); got != "" {
		t.Errorf("a ratified file was reported: %s", got)
	}
}

// A waiver that stops matching is reported, so an exemption cannot outlive the
// write it ratified.
func TestAWaiverForAFileThatNoLongerWritesTheColumnIsStale(t *testing.T) {
	rec := &recorder{TB: t}
	waivers := Waive(map[string]string{
		"gone.go": "this file once wrote an absolute expiry a provider returned, which is nobody's clock of ours",
	})
	DatabaseClock{Dir: packageWith(t, compliantStore), Column: "next_sync_at", Exempt: waivers}.Require(rec)
	waivers.AssertAllMatched(rec)
	if !strings.Contains(rec.joined(), "gone.go") {
		t.Errorf("a stale waiver was not reported: %q", rec.joined())
	}
}

// The mirror of the case above: on a line carrying several assignments, a bad
// write of the gated column must still be reported — the neighbours are what
// gets trimmed away, not the verdict.
func TestABadWriteSharingItsLineWithOthersIsStillReported(t *testing.T) {
	const source = "package store\n\nconst q = `\n\tUPDATE sync_state SET next_sync_at = $2, claimed_until = NULL, updated_at = now()\n\tWHERE id = $1`\n"
	got := reportOn(t, source, "next_sync_at")
	if !strings.Contains(got, "`next_sync_at = $2`") {
		t.Errorf("a bad write sharing its line escaped the gate: %q", got)
	}
}

// A clamp PICKS one of its arguments rather than computing a value, so it is
// database-clocked exactly when everything it may pick is. Sessions bound the
// idle window by the absolute one this way, and both terms are Postgres's.
func TestAClampOverDatabaseClockedTermsIsAccepted(t *testing.T) {
	const source = "package store\n\nconst q = `\n\tUPDATE session SET idle_expires_at = least(now() + $2::interval, expires_at)\n\tWHERE id = $1`\n"
	if got := reportOn(t, source, "idle_expires_at"); got != "" {
		t.Errorf("a clamp over database-clocked terms was reported: %s", got)
	}
}

// And the mirror: a clamp is only as good as its worst argument.
func TestAClampHidingABoundInstantIsReported(t *testing.T) {
	const source = "package store\n\nconst q = `\n\tUPDATE session SET idle_expires_at = least($2, expires_at)\n\tWHERE id = $1`\n"
	got := reportOn(t, source, "idle_expires_at")
	if !strings.Contains(got, "least($2, expires_at)") {
		t.Errorf("a bound instant inside a clamp escaped the gate: %q", got)
	}
}

// A function this rule cannot reason about is not a clamp, whatever it wraps.
func TestAnUnknownFunctionIsNotSeenThrough(t *testing.T) {
	const source = "package store\n\nconst q = `UPDATE session SET expires_at = date_trunc('day', now())`\n"
	got := reportOn(t, source, "expires_at")
	if !strings.Contains(got, "date_trunc") {
		t.Errorf("an unrecognised function was seen through: %q", got)
	}
}

// Forwarding a column the row already holds is the sibling column's obligation,
// not this write's — but EXCLUDED is not such a column. It carries whatever the
// INSERT proposed, so accepting it would let any conflict clause launder a
// cross-clock write.
func TestForwardingAStoredColumnIsAcceptedButExcludedIsNot(t *testing.T) {
	const stored = "package store\n\nconst q = `UPDATE agent_task SET expires_at = approval_expires_at WHERE id = $1`\n"
	if got := reportOn(t, stored, "expires_at"); got != "" {
		t.Errorf("forwarding a stored column was reported: %s", got)
	}

	const excluded = "package store\n\nconst q = `\n\tINSERT INTO grant_row (id, expires_at) VALUES ($1, $2)\n\tON CONFLICT (id) DO UPDATE SET expires_at = EXCLUDED.expires_at`\n"
	got := reportOn(t, excluded, "expires_at")
	if !strings.Contains(got, "EXCLUDED.expires_at") {
		t.Errorf("a conflict clause laundered a bound instant: %q", got)
	}
}
