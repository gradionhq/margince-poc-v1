// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package runner

import (
	"context"
	"testing"
	"time"
)

// A non-positive grace turns the abandoned-run sweep into "fail every running
// run in this workspace", which is one edit away: the caller derives the grace
// from a constant, and a change there lands in production with nothing between.
// The guard refuses before the statement is built, which is why a store with no
// pool is enough to pin it — reaching the database would itself be the failure.
func TestASweepWithNoGraceIsRefusedRatherThanFailingEveryRunningRun(t *testing.T) {
	s := NewStore(nil)

	for _, grace := range []time.Duration{0, -time.Minute} {
		swept, err := s.FailStuckRuns(context.Background(), grace, "should never be written")
		if err == nil {
			t.Fatalf("a grace of %s was accepted; every executing run in the workspace would be failed", grace)
		}
		if swept != nil {
			t.Errorf("a refused sweep reported %d closed runs, want none", len(swept))
		}
	}
}
