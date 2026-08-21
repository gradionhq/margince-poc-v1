// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agentactivity

import "testing"

// The mapping is the highest-risk surface in this feature: a status that reads as
// `done` when the run stopped early tells a person a job finished that did not.
// So every value the CHECK constraints admit is named here, including the ones
// that must not surface at all.
func TestRunStatusMapsToTheStateAPersonWouldRecognise(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   State
		ok     bool
	}{
		{"running", StateRunning, true},
		{"awaiting_approval", StateAwaitingApproval, true},
		{"completed", StateDone, true},
		// Budget exhaustion keeps partial state. It is NOT done: saying so would
		// claim a completion that did not happen.
		{"degraded", StateDegraded, true},
		{"failed", StateFailed, true},
		{"", "", false},
		{"queued", "", false}, // a job status, never a run status
		{"nonsense", "", false},
	} {
		got, ok := RunState(tc.status)
		if ok != tc.ok || got != tc.want {
			t.Errorf("RunState(%q) = (%q, %v), want (%q, %v)", tc.status, got, ok, tc.want, tc.ok)
		}
	}
}

func TestJobStatusOnlySurfacesWhileItIsStillWaiting(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   State
		ok     bool
	}{
		{"queued", StateQueued, true},
		// A claimed or finished job has an agent_run row, and THAT row is the
		// truth. Surfacing both would report one occurrence twice.
		{"running", "", false},
		{"done", "", false},
		{"failed", "", false},
		{"", "", false},
	} {
		got, ok := JobState(tc.status)
		if ok != tc.ok || got != tc.want {
			t.Errorf("JobState(%q) = (%q, %v), want (%q, %v)", tc.status, got, ok, tc.want, tc.ok)
		}
	}
}

func TestDegradedIsNeverDone(t *testing.T) {
	degraded, _ := RunState("degraded")
	done, _ := RunState("completed")
	if degraded == done {
		t.Fatal("degraded and completed must not share a state: one kept partial work, the other finished")
	}
}
