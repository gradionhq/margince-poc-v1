// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package capture

// The running page's tally lives in the inflight_* columns and is added to the
// committed counters by the status read, so exactly one writer may SET it from
// its parameters and every other writer must ZERO it. A new terminal write that
// forgets the reset would not fail any behavioural test — it would quietly
// report the page's work twice — so the obligation is derived from the source
// rather than maintained as a list of five call sites.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// backfillRunUpdate is the statement prefix every writer of a run row shares.
const backfillRunUpdate = "UPDATE capture_backfill"

// statementWindow is how far past the UPDATE the SET clause can reach. The
// longest statement in the package is well inside it; a statement that grew
// past it would fail this test rather than pass silently.
const statementWindow = 16

func TestEveryBackfillRunWriteSettlesTheInFlightTally(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading the package directory: %v", err)
	}
	var writers int
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		writers += auditRunWrites(t, name, string(src))
	}
	if writers != 1 {
		t.Fatalf("found %d statements that SET inflight_scanned from a parameter, want exactly 1 (flushBackfillProgress) — two live writers of the same transient tally cannot both be right", writers)
	}
}

// auditRunWrites checks every capture_backfill UPDATE in one file and returns
// how many of them are the live writer.
func auditRunWrites(t *testing.T, file, src string) int {
	t.Helper()
	lines := strings.Split(src, "\n")
	var writers int
	for i, line := range lines {
		if !strings.Contains(line, backfillRunUpdate) {
			continue
		}
		end := min(i+statementWindow, len(lines))
		stmt := strings.Join(lines[i:end], "\n")
		switch {
		case strings.Contains(stmt, "inflight_scanned = $"):
			writers++
		case strings.Contains(stmt, resetInflightProgressMarker):
			// Ends the page and clears the tally with it.
		default:
			t.Errorf("%s:%d writes a capture_backfill run row without settling the running page's tally — end it with resetInflightProgress, or the status read counts that page twice:\n%s",
				file, i+1, stmt)
		}
	}
	return writers
}

// resetInflightProgressMarker is how the reset appears in SOURCE: the fragment
// is a const concatenated into each statement, so the columns it names are not
// literally in the string the writer spells.
const resetInflightProgressMarker = "resetInflightProgress"
