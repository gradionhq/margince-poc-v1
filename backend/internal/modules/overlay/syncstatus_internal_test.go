// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

// aggregateState's own unit-level proof: the worst-state-wins ordering
// (pending outranks stale outranks fresh) SyncStatus's aggregate query
// relies on, isolated from the real-Postgres aggregate query itself
// (proven by the package's own integration suite).

import "testing"

func TestAggregateStateWorstWins(t *testing.T) {
	tests := []struct {
		name                 string
		anyPending, anyStale bool
		want                 string
	}{
		{"neither -> fresh", false, false, syncStateFresh},
		{"stale only -> stale", false, true, syncStateStale},
		{"pending only -> pending", true, false, syncStatePendingSync},
		{"both -> pending outranks stale", true, true, syncStatePendingSync},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := aggregateState(mirrorStateFlags{anyPending: tt.anyPending, anyStale: tt.anyStale})
			if got != tt.want {
				t.Errorf("aggregateState(pending=%v, stale=%v) = %q, want %q", tt.anyPending, tt.anyStale, got, tt.want)
			}
		})
	}
}
