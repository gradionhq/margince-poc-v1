// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package jobs

// The disposition half of the fault seam: whether a classified failure FAILS the
// tick or postpones it.
//
// These cases exist because the two answers are indistinguishable in the type
// system — both are an `error` a worker returns — and completely different to an
// operator. A failure spends the child's attempts and becomes dead work on the
// Maintenance screen; a postponement reschedules the same row and shows nobody
// anything. Returning the wrong one either fills a screen with alarms nobody can
// act on, or hides an outage somebody has to.

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/riverqueue/river"

	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/pkg/extension"
)

// transientClass is the class these cases postpone on, spelled once. It is the
// shape both shipped connectors declare: a provider nobody can reach, whose own
// remedy says that nothing needs doing.
var transientClass = extension.FailureClass{
	Class:    "provider_unavailable",
	Sentence: "the provider could not be reached from this installation",
	Remedy:   "Nothing to do: the poll catches up by itself and no message is lost.",
}

// TestATransientFailurePostponesTheTickInsteadOfFailingIt is the whole point of
// the change: the same cause that used to spend three attempts and leave a dead
// row now reschedules, and the delay the unit asked for is the delay River gets.
func TestATransientFailurePostponesTheTickInsteadOfFailingIt(t *testing.T) {
	t.Cleanup(resetComposedFailureClasses)
	registerForTest(t, transientClass)

	cause := errors.New("dial tcp: lookup openapi.example: no such host")
	returned := FaultForKind(t.Context(), unitKind,
		extension.Reschedule(transientClass, 120*time.Second, cause))

	var snooze *river.JobSnoozeError
	if !errors.As(returned, &snooze) {
		t.Fatalf("returned %v, want a river.JobSnoozeError — a failure River can retry, not one it discards", returned)
	}
	if snooze.Duration != 120*time.Second {
		t.Fatalf("postponed for %s, want the 120s the unit asked for", snooze.Duration)
	}
}

// TestAPostponementPublishesNothingIntoTheFailureColumn.
//
// River records no attempt error for a snooze, so nothing the cause said should
// be reachable through what the worker returns either. This is the same
// obligation the sentence substitution carries on the failing path, checked on
// the path that has no substitution to do it.
func TestAPostponementPublishesNothingIntoTheFailureColumn(t *testing.T) {
	t.Cleanup(resetComposedFailureClasses)
	registerForTest(t, transientClass)

	returned := FaultForKind(t.Context(), unitKind, extension.Reschedule(transientClass, time.Minute,
		errors.New("dial tcp: lookup openapi.example: no such host")))

	if strings.Contains(returned.Error(), "openapi.example") {
		t.Fatalf("the cause's own text reached what the worker returns: %q", returned.Error())
	}
}

// TestAPostponementIsRefusedForAClassThisInstallationNeverDeclared.
//
// Declaring a class is what makes it honoured, and the disposition is held to the
// same rule as the sentence rather than to a second one. A unit that forgot to
// declare gets the behaviour it had before it asked — a failure — which is the
// safe direction: an undeclared postponement would let a unit silence its own
// dead work by asking nicely.
func TestAPostponementIsRefusedForAClassThisInstallationNeverDeclared(t *testing.T) {
	t.Cleanup(resetComposedFailureClasses)
	registerForTest(t, transientClass)

	undeclared := extension.FailureClass{
		Class:    "provider_shrugged",
		Sentence: "the provider shrugged",
		Remedy:   "wait",
	}
	for _, tc := range []struct {
		name, kind string
		class      extension.FailureClass
	}{
		{"a class this installation never declared", unitKind, undeclared},
		{"a declared class under another kind", otherKind, transientClass},
		{"no kind at all, as FaultContext has none", "", transientClass},
	} {
		t.Run(tc.name, func(t *testing.T) {
			returned := FaultForKind(t.Context(), tc.kind,
				extension.Reschedule(tc.class, time.Minute, errors.New("upstream said no")))

			var snooze *river.JobSnoozeError
			if errors.As(returned, &snooze) {
				t.Fatalf("postponed on %s — an unverified class must not choose its own disposition", tc.name)
			}
			if returned.Error() != unrecognised {
				t.Fatalf("persisted %q, want the unclassified substitute", returned.Error())
			}
		})
	}
}

// TestAPostponementIsHeldToTheSeamsOwnBounds.
//
// The delay is a REQUEST, like the class is, and the two bounds answer two
// different hazards. A negative duration PANICS inside River, so an unbounded
// one takes the worker process down rather than failing a tick. An enormous one
// does the opposite: it takes the row off every screen that shows live work
// without failing anything an operator could find, which is the silent outage
// this area exists to prevent.
func TestAPostponementIsHeldToTheSeamsOwnBounds(t *testing.T) {
	t.Cleanup(resetComposedFailureClasses)
	registerForTest(t, transientClass)

	for _, tc := range []struct {
		name string
		ask  time.Duration
		want time.Duration
	}{
		{"a negative delay, which River itself panics on", -time.Hour, minRescheduleDelay},
		{"zero, which River reads as run-me-immediately", 0, minRescheduleDelay},
		{"a connector's own cadence, honoured as asked", 120 * time.Second, 120 * time.Second},
		{"a delay past the ceiling", 72 * time.Hour, maxRescheduleDelay},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logged bytes.Buffer
			restore := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelWarn})))
			t.Cleanup(func() { slog.SetDefault(restore) })

			returned := FaultForKind(t.Context(), unitKind,
				extension.Reschedule(transientClass, tc.ask, errors.New("provider unreachable")))

			var snooze *river.JobSnoozeError
			if !errors.As(returned, &snooze) {
				t.Fatalf("returned %v, want a postponement", returned)
			}
			if snooze.Duration != tc.want {
				t.Fatalf("postponed for %s, want %s", snooze.Duration, tc.want)
			}
			// A CLAMP THAT SAID NOTHING would be a bound that catches the mistake
			// it exists for and leaves no trace of it: 72h and 15m would log
			// identically, so nobody could tell a unit computing a delay wrong
			// from one asking for the ceiling on purpose.
			asked := strings.Contains(logged.String(), "requested="+tc.ask.String())
			if want := tc.ask != tc.want; asked != want {
				t.Fatalf("the log names the original request = %v, want %v: %q", asked, want, logged.String())
			}
		})
	}
}

// TestAPostponementLogsAtWarnWithItsCauseAndItsDelay.
//
// A snooze leaves NO attempt error on the row, so this line plus the unit's own
// connection row are the entire trail an outage leaves in the process. It is
// Warn rather than Error because a postponed tick is not a failure — putting it
// in the same lane as dead work is how a routine outage starts paging somebody —
// and it must carry the cause, because the class says what kind of thing broke
// and never which host did not resolve.
func TestAPostponementLogsAtWarnWithItsCauseAndItsDelay(t *testing.T) {
	t.Cleanup(resetComposedFailureClasses)
	registerForTest(t, transientClass)

	var logged bytes.Buffer
	restore := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(restore) })

	returned := FaultForKind(t.Context(), unitKind, extension.Reschedule(transientClass, 120*time.Second,
		errors.New("dial tcp: lookup openapi.example: no such host")))

	var snooze *river.JobSnoozeError
	if !errors.As(returned, &snooze) {
		t.Fatalf("returned %v, want a postponement — the log line below is only the postponed path's", returned)
	}
	line := logged.String()
	for _, want := range []struct{ what, substring string }{
		{"the level, so a postponement does not read as dead work", "level=WARN"},
		{"the kind, so the line ties to the row an operator is reading", unitKind},
		{"the class", transientClass.Class},
		{"the cause, which is the diagnosis", "openapi.example"},
		{"the delay, so a connector postponing itself for a day is visible", "retry_in=2m0s"},
	} {
		if !strings.Contains(line, want.substring) {
			t.Errorf("the log line carries no %s (%q): %q", want.what, want.substring, line)
		}
	}
}

// TestAPostponementIsNotAvailableToTheCoreVocabulary.
//
// The core half classifies by SENTINEL and has no disposition field, so a core
// sentinel cannot ask to be postponed. Stated as a case rather than left to be
// inferred: the two halves render through one screen and it should be provable
// that they cannot diverge on what a failure DOES, only on what it is called.
func TestAPostponementIsNotAvailableToTheCoreVocabulary(t *testing.T) {
	t.Cleanup(resetComposedFailureClasses)

	returned := FaultForKind(t.Context(), unitKind, apperrors.ErrConflict)

	var snooze *river.JobSnoozeError
	if errors.As(returned, &snooze) {
		t.Fatalf("a core sentinel postponed itself — only a declared composed class may choose that disposition")
	}
}

// TestAnOrdinaryClassifiedFailureStillFails guards the asymmetry the change
// turns on. Only the class that asked is postponed; a rejected credential, a
// lapsed package and an unreadable answer all still become dead work, because
// each of them needs somebody.
func TestAnOrdinaryClassifiedFailureStillFails(t *testing.T) {
	t.Cleanup(resetComposedFailureClasses)
	registerForTest(t, transientClass)

	returned := FaultForKind(t.Context(), unitKind,
		extension.Failure(transientClass, errors.New("provider unreachable")))

	var snooze *river.JobSnoozeError
	if errors.As(returned, &snooze) {
		t.Fatalf("a plain classified failure postponed itself — the disposition must come from the unit's own return, not from its class")
	}
	if returned.Error() != transientClass.Sentence {
		t.Fatalf("persisted %q, want the declared sentence %q", returned.Error(), transientClass.Sentence)
	}
}
