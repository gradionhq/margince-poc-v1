// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"testing"

	"github.com/riverqueue/river/rivertype"
)

func TestJobKindsAreStable(t *testing.T) {
	if got := (CloseDateSweepArgs{}).Kind(); got != "close_date_sweep" {
		t.Errorf("CloseDateSweepArgs.Kind() = %q, want close_date_sweep", got)
	}
	if got := (FollowUpReconcileArgs{}).Kind(); got != "follow_up_reconcile" {
		t.Errorf("FollowUpReconcileArgs.Kind() = %q, want follow_up_reconcile", got)
	}
	if got := (TimeScanArgs{}).Kind(); got != "time_scan" {
		t.Errorf("TimeScanArgs.Kind() = %q, want time_scan", got)
	}
}

// TestUniquenessWindowExcludesCompleted is the load-bearing invariant: the
// periodic passes suppress a duplicate only while a prior run is in flight,
// never after it completes — otherwise a completed 24h sweep would block the
// next day's run until the completed row is cleaned out. It must also keep
// the states River requires when ByState is set.
func TestUniquenessWindowExcludesCompleted(t *testing.T) {
	have := map[rivertype.JobState]bool{}
	for _, s := range activeSweepStates {
		have[s] = true
	}

	if have[rivertype.JobStateCompleted] {
		t.Error("activeSweepStates includes JobStateCompleted — a completed sweep would block the next scheduled run")
	}

	// River requires these states whenever ByState is set explicitly.
	for _, required := range []rivertype.JobState{
		rivertype.JobStateAvailable,
		rivertype.JobStatePending,
		rivertype.JobStateRunning,
		rivertype.JobStateScheduled,
	} {
		if !have[required] {
			t.Errorf("activeSweepStates omits required state %q", required)
		}
	}
}
