// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package migrations

// A migration that takes a lock strong enough to block writers must bound how
// long it will WAIT for it.
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
// SHARE ROW EXCLUSIVE on `relationship` without it, and nothing noticed. Three
// hand-kept precedents and one miss is the shape CLAUDE.md's "prefer fitness
// functions over point fixes" rule exists for, so the obligation is derived from
// the files rather than remembered.

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// blockingLock matches the lock levels that conflict with ordinary INSERT /
// UPDATE / DELETE (ROW EXCLUSIVE). Weaker ones do not queue writers, so they
// carry no obligation. ALTER TABLE and CREATE/DROP INDEX take ACCESS EXCLUSIVE
// implicitly and are the reason this is not just a `LOCK TABLE` grep.
var blockingLock = regexp.MustCompile(
	`(?i)\bLOCK\s+TABLE\b|\bACCESS\s+EXCLUSIVE\b|\bSHARE\s+ROW\s+EXCLUSIVE\b|\bSHARE\s+UPDATE\s+EXCLUSIVE\b`)

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

// checkLockTimeouts reads every .sql file in a namespace and reports the ones
// that take a writer-blocking lock without bounding the wait.
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
		// Comments discuss locks; only statements take them. Stripping them is
		// what keeps 0116 — which mentions lock_timeout in prose while taking
		// nothing — from being reported.
		statements := withoutComments(string(body))
		if !blockingLock.MatchString(statements) {
			return nil
		}
		if !strings.Contains(statements, "lock_timeout") {
			t.Errorf("%s/%s takes a lock that blocks writers and never bounds the wait.\n"+
				"Add `SET LOCAL lock_timeout = '3s';` before it, as core/0139 does and explains: "+
				"without it one open transaction stalls every write to that table for as long as "+
				"this migration is willing to queue, which is forever.", namespace, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("reading the migration files: %v", err)
	}
}

// withoutComments drops `--` line comments so prose about locking is not read
// as locking. Migrations in this tree carry long explanatory headers, and every
// one of them would otherwise be a false positive.
func withoutComments(sql string) string {
	lines := strings.Split(sql, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if idx := strings.Index(line, "--"); idx >= 0 {
			line = line[:idx]
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}
