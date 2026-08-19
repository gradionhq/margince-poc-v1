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
		regexp.MustCompile(`(?is)\bLOCK\s+(?:TABLE\s+)?([\w".]+)(?:\s+IN\s+(?:ACCESS\s+EXCLUSIVE|EXCLUSIVE|SHARE\s+ROW\s+EXCLUSIVE|SHARE\s+UPDATE\s+EXCLUSIVE|SHARE)\s+MODE)?\s*(?:;|$)`),
	}

	// unresolvableBlockers act on a pre-existing object whose table this cannot
	// name — DROP INDEX names the index, not what it indexes. A migration only
	// drops an index that already shipped, so the table is by definition one
	// other sessions can see: always report. That is exactly the 0139 case, and
	// 0139 sets the timeout.
	unresolvableBlockers = regexp.MustCompile(`(?is)\bDROP\s+INDEX\b|\bREINDEX\b`)
)

// lockTimeoutBaseline is where this obligation starts. Migrations sorting at or
// after it must comply; earlier ones are a backlog this gate cannot clear.
//
// Arming a rule from a baseline rather than retroactively is how this repo
// already does it — CLAUDE.md describes clearing the tree to zero before arming
// craft-static's bar. Here the tree CANNOT be cleared in this change: ~100
// migrations take a blocking lock on a pre-existing table without a timeout,
// most of them long applied everywhere, and editing applied migrations to add
// timeouts they will never re-run with would be churn with no effect on any
// deployed database. The backlog is filed; what this stops is the NEXT miss,
// which is the one that can still be prevented.
//
// PER NAMESPACE, because the two number their versions differently — core by a
// unix-second stamp, custom by a UTC timestamp (ADR-0017) — and one string
// compared across both would put every `2026…` custom file after every
// `1787…` core one and arm custom's whole backlog by accident.
//
// core's baseline is 1787111736, the migration whose missing timeout prompted the
// rule and which is fixed in the same change that arms it. custom's is the next
// stamp after its newest file, so the rule binds what is written from here.
var lockTimeoutBaseline = map[string]string{
	"core":   "1787111736",
	"custom": "20260817110001",
}

// setsLockTimeout matches the STATEMENT, not the word. A file that merely
// mentions lock_timeout in prose or a string literal, or sets it after the lock
// it was meant to bound, is a file with no timeout — so the check is positional.
var setsLockTimeout = regexp.MustCompile(`(?is)\bSET\s+(LOCAL\s+)?lock_timeout\s*=`)

func TestEveryMigrationTakingABlockingLockBoundsHowLongItWaits(t *testing.T) {
	// Derived from the embedded tree, not from a list: a namespace added later
	// is covered without anybody remembering to add it here.
	for _, namespace := range []string{"core", "custom"} {
		t.Run(namespace, func(t *testing.T) {
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
	err := fs.WalkDir(dir, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".sql") {
			return err
		}
		body, readErr := fs.ReadFile(dir, path)
		if readErr != nil {
			return readErr
		}
		if version(path) < lockTimeoutBaseline[namespace] {
			return nil
		}
		if reason := unboundedLock(string(body)); reason != "" {
			t.Errorf("%s/%s %s.\nAdd `SET LOCAL lock_timeout = '3s';` before it, as core/0139 does "+
				"and explains: without it one open transaction stalls every write to that table for "+
				"as long as this migration is willing to queue, which is forever.", namespace, path, reason)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("reading the migration files: %v", err)
	}
}

// unboundedLock names what is wrong with one file, or returns "" when the file
// takes no lock other sessions could be waiting behind, or has already bounded
// it. The ORDER matters as much as the presence: a timeout set after the lock
// bounds nothing.
func unboundedLock(sql string) string {
	statements := withoutComments(sql)
	own := map[string]bool{}
	for _, m := range createsTable.FindAllStringSubmatch(statements, -1) {
		own[strings.ToLower(strings.Trim(m[2], `"`))] = true
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

// firstBlockingIndex returns where the earliest reportable blocking statement
// starts, or -1. A statement aimed at a table this same file creates is skipped:
// nothing else can be holding a lock on a table that did not exist a moment ago.
func firstBlockingIndex(statements string, own map[string]bool) int {
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
			table := strings.ToLower(strings.Trim(statements[loc[2]:loc[3]], `"`))
			if !own[table] {
				note(loc[0])
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

// withoutComments drops `--` line comments so prose about locking is not read as
// locking — the migrations here carry long explanatory headers and every one of
// them would otherwise be a false positive.
//
// Quoted strings are respected. A `--` inside one is data, not a comment, and
// truncating there would hide every statement after it on the line: the shape
// `PERFORM '--'; LOCK TABLE …` would read as a file that takes no lock at all.
func withoutComments(sql string) string {
	var out strings.Builder
	out.Grow(len(sql))
	inString := false
	for i := 0; i < len(sql); i++ {
		switch {
		case sql[i] == '\'':
			inString = !inString
			out.WriteByte(sql[i])
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
	} {
		t.Run(probe.name, func(t *testing.T) {
			reason := unboundedLock(probe.sql)
			if (reason != "") != probe.reported {
				t.Errorf("reported=%t, want %t (reason %q)", reason != "", probe.reported, reason)
			}
		})
	}
}
