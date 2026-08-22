// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package deals

import (
	"testing"
	"time"
)

// The fold is what the project page answers "how long were we selling versus
// delivering" with, so it is held to the three things a reader relies on:
// each phase's seconds are the gaps between consecutive transitions, the open
// phase runs to the instant given, and a revisited phase is summed rather
// than listed twice.

var foldT0 = time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)

func at(hours int) time.Time { return foldT0.Add(time.Duration(hours) * time.Hour) }

func transition(from string, to string, hours int) PhaseTransition {
	t := PhaseTransition{ToPhase: to, OccurredAt: at(hours)}
	if from != "" {
		t.FromPhase = &from
	}
	return t
}

func TestFoldPhaseDurationsMeasuresEachStayAndLeavesTheCurrentOneOpen(t *testing.T) {
	history := []PhaseTransition{
		transition("", PhaseInitiative, 0),
		transition(PhaseInitiative, "pursuing", 2),
		transition("pursuing", "delivering", 12),
	}
	got := FoldPhaseDurations(history, at(20))
	want := []PhaseDuration{
		{Phase: PhaseInitiative, Seconds: 2 * 3600},
		{Phase: "pursuing", Seconds: 10 * 3600},
		{Phase: "delivering", Seconds: 8 * 3600, Current: true},
	}
	assertDurations(t, got, want)
}

func TestFoldPhaseDurationsSumsARevisitedPhase(t *testing.T) {
	// Closed, then re-opened into pursuing, then closed again: pursuing is
	// one entry carrying both stays, and closed is current with only its
	// second stay still open.
	history := []PhaseTransition{
		transition("", PhaseInitiative, 0),
		transition(PhaseInitiative, "pursuing", 1),
		transition("pursuing", PhaseClosed, 4),
		transition(PhaseClosed, "pursuing", 6),
		transition("pursuing", PhaseClosed, 9),
	}
	got := FoldPhaseDurations(history, at(10))
	want := []PhaseDuration{
		{Phase: PhaseInitiative, Seconds: 1 * 3600},
		{Phase: "pursuing", Seconds: (3 + 3) * 3600},
		{Phase: PhaseClosed, Seconds: (2 + 1) * 3600, Current: true},
	}
	assertDurations(t, got, want)
}

func TestFoldPhaseDurationsOnAnEmptyHistoryIsEmptyNotNil(t *testing.T) {
	got := FoldPhaseDurations(nil, at(0))
	if got == nil || len(got) != 0 {
		t.Fatalf("fold of no history = %#v, want an empty (non-nil) slice so the wire carries [] rather than null", got)
	}
}

func TestFoldPhaseDurationsNeverReportsANegativeStay(t *testing.T) {
	// A now earlier than the last transition — a clock skew between the
	// writer and the reader — clamps the open stay to zero rather than
	// reporting time the project spent in the future.
	history := []PhaseTransition{transition("", PhaseInitiative, 5)}
	got := FoldPhaseDurations(history, at(3))
	assertDurations(t, got, []PhaseDuration{{Phase: PhaseInitiative, Seconds: 0, Current: true}})
}

func assertDurations(t *testing.T, got, want []PhaseDuration) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("durations = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("durations[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}
