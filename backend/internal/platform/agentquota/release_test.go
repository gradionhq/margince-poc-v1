// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package agentquota

import (
	"strings"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

// The two quotas the spec ends WITH THE WINDOW cannot be widened from inside
// it. A release of egress would reopen the exfiltration endpoint the moment it
// closed; a release of calls would lift the ceiling every other quota sits
// under. Both are caller defects, answered as errors rather than as quiet
// successes — a release that silently did nothing would read, at the approval
// screen, exactly like one that worked.
func TestAHardStopCannotBeReleasedFromInsideItsOwnWindow(t *testing.T) {
	meter := New(nil, Limits{}, DefaultWindow)
	ws, passport := ids.New[ids.WorkspaceKind]().UUID, ids.New[ids.PassportKind]().UUID

	for _, c := range []Counter{Egress, Calls, Cost} {
		applied, err := meter.Release(t.Context(), ws, passport, c, meter.Bucket())
		if err == nil {
			t.Errorf("releasing %s was accepted; it is a hard stop the window alone ends", c)
		}
		if applied {
			t.Errorf("releasing %s reported that it applied", c)
		}
	}
}

// A release names the window the human was SHOWN. One answered after that
// window rolled applies to nothing — correctly, because the counter it would
// have widened is already back at zero and the agent is no longer refused — and
// it says so rather than claiming an effect it did not have.
func TestAReleaseAnsweredAfterItsWindowRolledAppliesToNothingAndSaysSo(t *testing.T) {
	now := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	meter := NewWithClock(nil, Limits{}, time.Hour, func() time.Time { return now })
	staged := meter.Bucket()
	now = now.Add(2 * time.Hour)

	applied, err := meter.Release(t.Context(), ids.New[ids.WorkspaceKind]().UUID,
		ids.New[ids.PassportKind]().UUID, Reads, staged)

	if err != nil {
		t.Fatalf("releasing a window that has already rolled is not an error: %v", err)
	}
	if applied {
		t.Error("a release into a window that has rolled reported that it applied")
	}
}

// The opposite direction is refused rather than ignored. The payload a release
// reads is stored on the approval row and the approvals module lets a human
// EDIT a staged proposal before approving it, so a bucket in the future is a
// value a caller can supply — and honouring one would pre-authorize a window
// nobody has looked at.
func TestAReleaseCannotPreAuthorizeAWindowThatHasNotStarted(t *testing.T) {
	meter := New(nil, Limits{}, DefaultWindow)

	applied, err := meter.Release(t.Context(), ids.New[ids.WorkspaceKind]().UUID,
		ids.New[ids.PassportKind]().UUID, Reads, meter.Bucket()+1)

	if err == nil {
		t.Fatal("a release into a future window was accepted; it pre-authorizes a window nobody has seen")
	}
	if applied {
		t.Error("a future-window release reported that it applied")
	}
}

// Release takes its subject explicitly because it runs as the HUMAN, in a
// request the metered agent is nowhere near. A missing half of that subject is a
// wiring fault and must fail loudly: a release recorded against the zero uuid
// would widen a window no agent owns, and the agent that was refused would stay
// refused with an approval that reads as granted.
func TestAReleaseWithNoSubjectIsAWiringFaultRatherThanAQuietNoOp(t *testing.T) {
	meter := New(nil, Limits{}, DefaultWindow)
	ws, passport := ids.New[ids.WorkspaceKind]().UUID, ids.New[ids.PassportKind]().UUID

	for _, c := range []struct {
		name         string
		ws, passport ids.UUID
	}{
		{"no workspace", ids.UUID{}, passport},
		{"no passport", ws, ids.UUID{}},
	} {
		_, err := meter.Release(t.Context(), c.ws, c.passport, Reads, meter.Bucket())
		if err == nil || !strings.Contains(err.Error(), "needs both a workspace and a passport") {
			t.Errorf("%s: released anyway (%v)", c.name, err)
		}
	}
}

// A composition that declared no bound has nothing to widen, and one that
// cannot reach Redis is refusing everything already — neither may report a
// release as applied, because the decision path prints that answer to the human
// who just pressed approve.
func TestAReleaseReportsItselfUnappliedWhenThereIsNoCounterToWiden(t *testing.T) {
	ws, passport := ids.New[ids.WorkspaceKind]().UUID, ids.New[ids.PassportKind]().UUID

	for name, meter := range map[string]*Meter{
		"declared no bound":        Unmetered(),
		"cannot reach its counter": New(nil, Limits{}, DefaultWindow),
	} {
		applied, err := meter.Release(t.Context(), ws, passport, Reads, meter.Bucket())
		if err != nil {
			t.Errorf("%s: releasing failed rather than reporting no effect: %v", name, err)
		}
		if applied {
			t.Errorf("%s: reported a release that widened nothing", name)
		}
	}
}

// One release grants ONE more allowance of the same size — the question the
// approval screen actually asked. A release that granted an unbounded
// continuation would make the second confirmation the last one anyone is ever
// asked for.
func TestOneReleaseGrantsOneMoreAllowanceAndNotAnUnboundedOne(t *testing.T) {
	meter := New(nil, Limits{Reads: 100}, DefaultWindow)

	for released, want := range map[int]int{0: 100, 1: 200, 3: 400} {
		if got := meter.effectiveLimit(Reads, released); got != want {
			t.Errorf("%d releases raised the 100-record limit to %d, want %d", released, got, want)
		}
	}
	if got := meter.effectiveLimit(Reads, -1); got != 100 {
		t.Errorf("a negative release count raised the limit to %d; it must never lower or widen it", got)
	}
}
