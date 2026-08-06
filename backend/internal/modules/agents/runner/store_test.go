// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package runner

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A grace that reaches the statement as zero turns the abandoned-run sweep into
// "fail every running run in this workspace", which is one edit away: the caller
// derives the grace from a constant, and a change there lands in production with
// nothing between. The guard refuses before the statement is built, which is why
// a store with no pool is enough to pin it — reaching the database would itself
// be the failure.
func TestASweepWithNoUsableGraceIsRefusedRatherThanFailingEveryRunningRun(t *testing.T) {
	s := NewStore(nil)

	for _, tc := range []struct {
		name  string
		grace time.Duration
	}{
		{"zero", 0},
		{"negative", -time.Minute},
		// Positive in Go and zero in the statement: an interval is the finest
		// granularity Postgres can compare, so anything under a microsecond
		// truncates onto the everything-is-abandoned cutoff.
		{"under a microsecond", 500 * time.Nanosecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			swept, err := s.FailStuckRuns(context.Background(), tc.grace, "should never be written")
			if err == nil {
				t.Fatalf("a grace of %s was accepted; every executing run in the workspace would be failed", tc.grace)
			}
			// The error has to be the GUARD's. A store with no pool fails for a
			// second reason — WithWorkspaceTx refuses an unbound context — so
			// asserting only that something went wrong would pass with the guard
			// deleted, which is the one thing this test exists to catch.
			if !strings.Contains(err.Error(), "grace") {
				t.Errorf("a grace of %s was refused by something other than the grace check: %v", tc.grace, err)
			}
			if swept != nil {
				t.Errorf("a refused sweep reported %d closed runs, want none", len(swept))
			}
		})
	}
}
