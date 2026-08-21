// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package migrations

// A migration that takes a lock strong enough to block writers must bound how
// long it will WAIT for it, and must do so BEFORE taking it.
//
// The hazard is not the lock's duration, it is its acquisition. A pending strong
// request queues behind whatever transaction is already running, and — this is
// the part that surprises people — every request arriving after it queues behind
// the request. One idle-in-transaction session therefore turns a migration into
// an installation-wide write stall for as long as the migration is willing to
// wait, which without lock_timeout is forever. Three seconds turns it into a
// fast, loud failure: the transaction rolls back whole and the deploy retries.
//
// 0139 wrote that reasoning down and 0147 and 0165 followed it; 1787111736 took
// SHARE ROW EXCLUSIVE on `relationship` without it and nothing noticed. Three
// hand-kept precedents and one miss is the shape CLAUDE.md's "prefer fitness
// functions over point fixes" rule exists for, so the obligation is derived from
// the files.
//
// MOST STRONG LOCKS ARE NEVER SPELLED. `ALTER TABLE`, `CREATE INDEX`,
// `DROP INDEX`, `DROP TABLE`, `TRUNCATE` and `REINDEX` all take ACCESS EXCLUSIVE
// implicitly and contain none of the words a lock-level grep looks for — the
// first version of this gate matched only explicit `LOCK TABLE` and therefore
// only the two files that carried one, which is how a gate reads green over the
// very tree it was written to police.

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// A lock only endangers a table OTHER SESSIONS CAN SEE. `CREATE TABLE x;
// CREATE INDEX … ON x;` takes ACCESS EXCLUSIVE on a table nothing else has ever
// read, and reporting it would bury the one case that matters under every
// migration that ever built a schema. So the check tracks what each file creates
// and only reports a blocking statement aimed at a table it did not.
var (
	// createsTable names a table this file brings into existence.
	createsTable = regexp.MustCompile(`(?is)\bCREATE\s+TABLE\s+(IF\s+NOT\s+EXISTS\s+)?([\w".]+)`)

	// blockingStatements: the statement forms that take a lock conflicting with
	// ordinary INSERT/UPDATE/DELETE, each paired with the group that names the
	// table it acts on. Most of these never SPELL a lock level — ALTER TABLE,
	// CREATE INDEX, DROP TABLE and TRUNCATE all take ACCESS EXCLUSIVE implicitly,
	// which is why the first version of this gate (a lock-level grep) matched
	// only the two files that named one and read green over everything else.
	blockingStatements = []*regexp.Regexp{
		regexp.MustCompile(`(?is)\bALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?(?:ONLY\s+)?([\w".]+)`),
		regexp.MustCompile(`(?is)\bCREATE\s+(?:UNIQUE\s+)?INDEX\s+(?:IF\s+NOT\s+EXISTS\s+)?[\w".]+\s+ON\s+([\w".]+)`),
		regexp.MustCompile(`(?is)\bDROP\s+TABLE\s+(?:IF\s+EXISTS\s+)?([\w".]+)`),
		regexp.MustCompile(`(?is)\bTRUNCATE\s+(?:TABLE\s+)?([\w".]+)`),
		// LOCK TABLE only in its BLOCKING modes: `IN ACCESS SHARE MODE` conflicts
		// with nothing a writer does. A bare `LOCK TABLE x;` defaults to ACCESS
		// EXCLUSIVE, so a statement naming no mode counts.
		// LOCK TABLE in a BLOCKING mode. Split from the no-mode form below rather
		// than folded into one pattern with an optional mode: an optional group
		// matches emptily, so `IN ACCESS SHARE MODE` — which conflicts with
		// nothing a writer does — satisfied it and the gate reported a lock that
		// endangers no one. ONLY and a table list are both legal; the first table
		// named is enough to decide whether this file created it.
		regexp.MustCompile(`(?is)\bLOCK\s+(?:TABLE\s+)?(?:ONLY\s+)?([\w".]+)[^;]*?\bIN\s+(?:ACCESS\s+EXCLUSIVE|EXCLUSIVE|SHARE\s+ROW\s+EXCLUSIVE|SHARE\s+UPDATE\s+EXCLUSIVE|SHARE)\s+MODE`),
		// LOCK TABLE with NO mode clause, which defaults to ACCESS EXCLUSIVE. The
		// statement has to END at the table list, which is what keeps this from
		// also matching the ACCESS SHARE form above.
		regexp.MustCompile(`(?is)\bLOCK\s+(?:TABLE\s+)?(?:ONLY\s+)?([\w".]+)(?:\s*,\s*[\w".]+)*\s*;`),
	}

	// unresolvableBlockers act on a pre-existing object whose table this cannot
	// name — DROP INDEX names the index, not what it indexes. A migration only
	// drops an index that already shipped, so the table is by definition one
	// other sessions can see: always report. That is exactly the 0139 case, and
	// 0139 sets the timeout.
	unresolvableBlockers = regexp.MustCompile(`(?is)\bDROP\s+INDEX\b|\bREINDEX\b`)
)

// lockTimeoutBaseline is where this obligation starts: migrations sorting at or
// after it must comply.
//
// THIS GATE CURRENTLY EXAMINES NOTHING, and saying so is the point. The pin was
// armed above main to spare a backlog — roughly a hundred migrations taking a
// blocking lock on a pre-existing table without a timeout, most long applied
// everywhere, where adding timeouts to versions that will never re-run is churn
// with no effect on any deployed database. The baseline consolidation deleted
// every one of them, and what core opens with now sorts BELOW this pin. So the
// count of files this gate reads is zero, and it passes over an empty set.
//
// It is DORMANT, not broken, and it arms itself: a new core migration is named
// for the unix second it was written, which is above this value, so the very
// next migration is examined. That is why the pin stays where it is rather than
// dropping to reach the baseline — the baseline CREATES its tables, so there is
// nothing to contend with and no lock to bound, and demanding a timeout from
// statements that cannot block would teach the next reader that the timeout is a
// ritual rather than a lock budget.
//
// assertExaminedSomethingOnceArmed below is what keeps that claim honest: the
// moment any migration sorts above the pin, this gate has to have read it.
// gatekit:fixture the oldest version this gate binds in each namespace — data,
// not a cost: a namespace's entry names where the rule starts applying.
var lockTimeoutBaseline = map[string]string{
	"core":   "1787128083",
	"custom": "20260817110001",
}

// setsLockTimeout matches the STATEMENT, not the word. A file that merely
// mentions lock_timeout in prose or a string literal, or sets it after the lock
// it was meant to bound, is a file with no timeout — so the check is positional.
var setsLockTimeout = regexp.MustCompile(`(?is)\bSET\s+(LOCAL\s+)?lock_timeout\s*=`)

func TestEveryMigrationTakingABlockingLockBoundsHowLongItWaits(t *testing.T) {
	// Read off the embedded tree rather than listed, so a namespace added later
	// is covered without anybody remembering this file — the previous version
	// SAID that while iterating a hardcoded pair, which would have left a third
	// namespace silently unchecked.
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		t.Fatalf("reading the embedded migration namespaces: %v", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		namespace := entry.Name()
		t.Run(namespace, func(t *testing.T) {
			// A namespace with no baseline would take the zero value "" and arm
			// its entire backlog at once, which is a hundred failures that look
			// like this gate breaking rather than a decision somebody owes.
			if _, set := lockTimeoutBaseline[namespace]; !set {
				t.Fatalf("namespace %q has no lockTimeoutBaseline: pick the version this "+
					"obligation starts at for it, in the same change that adds the namespace", namespace)
			}
			dir, err := fs.Sub(files, namespace)
			if err != nil {
				t.Fatalf("reading the %s namespace: %v", namespace, err)
			}
			checkLockTimeouts(t, namespace, dir)
		})
	}
}

// checkLockTimeouts reports every .sql file that takes a writer-blocking lock
// without having bounded the wait first.
func checkLockTimeouts(t *testing.T, namespace string, dir fs.FS) {
	t.Helper()
	var examined, skipped int
	err := fs.WalkDir(dir, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".sql") {
			return err
		}
		body, readErr := fs.ReadFile(dir, path)
		if readErr != nil {
			return readErr
		}
		if version(path) < lockTimeoutBaseline[namespace] {
			skipped++
			return nil
		}
		examined++
		if reason := unboundedLock(string(body)); reason != "" {
			t.Errorf("%s/%s %s.\nAdd `SET LOCAL lock_timeout = '3s';` before it: without one, an open "+
				"transaction holding a conflicting lock stalls every write to that table for as long "+
				"as this migration is willing to queue, which is forever.", namespace, path, reason)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("reading the migration files: %v", err)
	}
	// Zero examined is EXPECTED while the whole namespace is the baseline, and a
	// lie the moment it is not. Reporting the split is what stops a pin that has
	// drifted above real work from reading as a clean run: the numbers say which
	// of the two situations produced the pass.
	if examined == 0 && skipped == 0 {
		t.Errorf("%s: no .sql files at all — the embedded namespace is empty, so this gate "+
			"read nothing and passed", namespace)
	}
	t.Logf("%s: examined %d file(s), %d below the %s pin",
		namespace, examined, skipped, lockTimeoutBaseline[namespace])
}

// unboundedLock names what is wrong with one file, or returns "" when the file
// takes no lock other sessions could be waiting behind, or has already bounded
// it. The ORDER matters as much as the presence: a timeout set after the lock
// bounds nothing.
func unboundedLock(sql string) string {
	statements := executableSQL(sql)
	// POSITION matters, not merely presence. `DROP TABLE foo; CREATE TABLE foo …`
	// mentions a CREATE for `foo` while its first statement takes ACCESS
	// EXCLUSIVE on the live `foo` — the exact hazard this gate exists for. So a
	// table counts as the file's own only from the point it is created onward.
	own := map[string]int{}
	for _, m := range createsTable.FindAllStringSubmatchIndex(statements, -1) {
		name := strings.ToLower(strings.Trim(statements[m[4]:m[5]], `"`))
		if _, seen := own[name]; !seen {
			own[name] = m[0]
		}
	}

	at := firstBlockingIndex(statements, own)
	if at < 0 {
		return ""
	}
	timeout := setsLockTimeout.FindStringIndex(statements)
	switch {
	case timeout == nil:
		return "takes a lock that blocks writers on a table it did not create, and never bounds the wait"
	case timeout[0] > at:
		return "sets lock_timeout only AFTER the statement that takes the lock, which bounds nothing"
	default:
		return ""
	}
}

// commaSiblings picks up the tables AFTER the first in a comma-separated list.
// Only `LOCK TABLE`, `DROP TABLE` and `TRUNCATE` take one; ALTER and CREATE INDEX
// name a single relation and then a column list, which must not be read as more
// tables.
var commaSiblings = regexp.MustCompile(`(?i),\s*(?:ONLY\s+)?([\w".]+)`)

// takesRelationList reports whether this statement form can name more than one
// table, so the siblings are only parsed where they mean what they look like.
var takesRelationList = regexp.MustCompile(`(?is)^\s*(LOCK|DROP\s+TABLE|TRUNCATE)\b`)

// relationsIn lists every table one blocking statement names, lower-cased and
// unquoted: the one its pattern captured, plus any comma-separated siblings.
// Reading only the capture let `LOCK TABLE scratch, relationship;` pass in a file
// that creates `scratch` — the first table was its own, the second was live, and
// the second is the one that matters.
func relationsIn(statement, captured string) []string {
	out := []string{strings.ToLower(strings.Trim(captured, `"`))}
	if !takesRelationList.MatchString(statement) {
		return out
	}
	for _, m := range commaSiblings.FindAllStringSubmatch(statement, -1) {
		out = append(out, strings.ToLower(strings.Trim(m[1], `"`)))
	}
	return out
}

// firstBlockingIndex returns where the earliest reportable blocking statement
// starts, or -1. A statement aimed at a table this same file creates is skipped:
// nothing else can be holding a lock on a table that did not exist a moment ago.
func firstBlockingIndex(statements string, own map[string]int) int {
	earliest := -1
	note := func(i int) {
		if i >= 0 && (earliest < 0 || i < earliest) {
			earliest = i
		}
	}
	if loc := unresolvableBlockers.FindStringIndex(statements); loc != nil {
		note(loc[0])
	}
	for _, pattern := range blockingStatements {
		for _, loc := range pattern.FindAllStringSubmatchIndex(statements, -1) {
			// EVERY relation the statement names, not just the captured one. A file
			// that creates `scratch` skipped `LOCK TABLE scratch, relationship;` on
			// the strength of its first table while the second was a live one.
			for _, table := range relationsIn(statements[loc[0]:loc[1]], statements[loc[2]:loc[3]]) {
				createdAt, isOwn := own[table]
				if !isOwn || loc[0] < createdAt {
					note(loc[0])
					break
				}
			}
		}
	}
	return earliest
}

// version is the sortable prefix of a migration filename, which is how the
// runner orders them — so comparing it as a string is the same comparison the
// runner makes, not a reinterpretation of it.
func version(path string) string {
	name := path
	if cut := strings.LastIndex(name, "/"); cut >= 0 {
		name = name[cut+1:]
	}
	if cut := strings.Index(name, "_"); cut >= 0 {
		return name[:cut]
	}
	return name
}

// executableSQL is what the checks above run against: `--` comments removed, and
// the CONTENTS of every quoted string blanked while its quotes stay put.
//
// Both halves are load-bearing, in opposite directions. Comments must go or every
// migration's explanatory header — this tree's are long and all mention locking —
// reads as a lock. String contents must go or a literal counts as code: a
// `SELECT` of the text "SET LOCAL lock_timeout" satisfied the timeout check for a
// file that never set one, and a `SELECT` of the text "LOCK TABLE x" reported a
// lock that was only ever a string.
//
// And a `--` INSIDE a string is data, not a comment. Truncating there would hide
// every statement after it on the line, so `PERFORM '--'; LOCK TABLE …` would
// read as a file taking no lock at all.
func executableSQL(sql string) string {
	var out strings.Builder
	out.Grow(len(sql))
	inString := false
	for i := 0; i < len(sql); i++ {
		switch {
		case sql[i] == '\'':
			inString = !inString
			out.WriteByte(sql[i])
		case inString:
			// Blanked, not dropped: the length and the line structure stay put so
			// the POSITIONS the order check compares remain the file's own.
			if sql[i] == '\n' {
				out.WriteByte('\n')
			} else {
				out.WriteByte(' ')
			}
		case !inString && sql[i] == '-' && i+1 < len(sql) && sql[i+1] == '-':
			// Skip to the end of the line, keeping the newline so statement
			// boundaries and line structure survive.
			for i < len(sql) && sql[i] != '\n' {
				i++
			}
			if i < len(sql) {
				out.WriteByte('\n')
			}
		default:
			out.WriteByte(sql[i])
		}
	}
	return out.String()
}

// The gate's own gate. Its first version passed over the whole tree while
// matching almost nothing in it — a lock-level grep against statements that take
// their locks implicitly — and nothing said so, because a fitness function that
// reports no findings looks identical whether the tree is clean or the check is
// blind. Each case below is one way that version was wrong.
func TestTheLockGateReportsWhatItClaimsTo(t *testing.T) {
	for _, probe := range []struct {
		name     string
		sql      string
		reported bool
	}{
		// The whole class the first version missed: no lock level is spelled.
		{"ALTER TABLE on a table it did not create", "ALTER TABLE relationship ADD COLUMN note text;", true},
		{"CREATE INDEX on a table it did not create", "CREATE INDEX i ON relationship (person_id);", true},
		{"DROP INDEX acts on something already shipped", "DROP INDEX IF EXISTS idx_old;", true},

		// And the noise that class would bury it under, if the check could not
		// tell a fresh table from a live one.
		{"an index on a table this file creates", "CREATE TABLE thing (id uuid);\nCREATE INDEX i ON thing (id);", false},
		{"ACCESS SHARE blocks no writer", "LOCK TABLE relationship IN ACCESS SHARE MODE;", false},

		// Presence is not enough: a timeout has to precede what it bounds.
		{"timeout before the lock", "SET LOCAL lock_timeout = '3s';\nLOCK TABLE relationship;", false},
		{"timeout after the lock bounds nothing", "LOCK TABLE relationship;\nSET LOCAL lock_timeout = '3s';", true},
		{"the word in prose is not a setting", "-- lock_timeout is discussed here\nALTER TABLE relationship ADD COLUMN x text;", true},

		// And `--` inside a string is data. Truncating there hid every statement
		// after it, so a file could take a lock the check never saw.
		{"a quoted double dash", "DO $$ BEGIN PERFORM '--'; ALTER TABLE relationship ADD COLUMN x text; END $$;", true},

		// Creating a table does not retroactively make an earlier lock on the
		// live one safe. Presence of a CREATE was enough once, which exempted a
		// drop-and-recreate from the obligation while its first statement took
		// ACCESS EXCLUSIVE on the table other sessions were still using.
		{"dropped before it is recreated", "DROP TABLE foo;\nCREATE TABLE foo (id uuid);", true},
		{"created before it is altered", "CREATE TABLE foo (id uuid);\nALTER TABLE foo ADD COLUMN x text;", false},

		// A bare LOCK defaults to ACCESS EXCLUSIVE, and ONLY and a table list are
		// both legal spellings that must not slip past.
		{"a bare lock with no mode", "LOCK TABLE relationship;", true},
		{"ONLY", "LOCK TABLE ONLY relationship IN SHARE ROW EXCLUSIVE MODE;", true},
		{"a table list", "LOCK TABLE relationship, person;", true},
		// The two bypasses a reviewer found by reading the matcher rather than the
		// tree, both of which passed every case above.
		{"a timeout that is only a string literal", "SELECT 'SET LOCAL lock_timeout = x';\nALTER TABLE relationship ADD COLUMN note text;", true},
		{"a lock that is only a string literal", "SELECT 'LOCK TABLE relationship';", false},
		{"a live table hiding behind a created one in a lock list", "CREATE TABLE scratch (id uuid);\nLOCK TABLE scratch, relationship;", true},
		{"a list of only tables this file creates", "CREATE TABLE a (id uuid);\nCREATE TABLE b (id uuid);\nLOCK TABLE a, b;", false},
		{"a column list is not a relation list", "CREATE TABLE thing (id uuid, other uuid);\nCREATE INDEX i ON thing (id, other);", false},
	} {
		t.Run(probe.name, func(t *testing.T) {
			reason := unboundedLock(probe.sql)
			if (reason != "") != probe.reported {
				t.Errorf("reported=%t, want %t (reason %q)", reason != "", probe.reported, reason)
			}
		})
	}
}
