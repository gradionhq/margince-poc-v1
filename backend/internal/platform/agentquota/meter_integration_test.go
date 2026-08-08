// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package agentquota

import (
	"context"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/platform/overlaybudget/budgettest"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// The Redis fixture is budgettest's: it is the platform tier's shared
// flushed-client helper (isolated db, fails loudly rather than skipping), and
// it is named after the meter it was written for rather than after what it
// does. Re-reading MARGINCE_TEST_REDIS here would be a second spelling of the
// same fixture for no gain.

// meteredCall builds a context for one Passport inside workspace ws. The
// workspace is a PARAMETER because the isolation test has to hold it fixed:
// minting a fresh one per call would separate the two Passports by workspace as
// well, and the test would pass without ever proving per-Passport isolation.
func meteredCall(t *testing.T, ws ids.UUID, passport ids.UUID) context.Context {
	t.Helper()
	ctx := principal.WithWorkspaceID(t.Context(), ws)
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:reader", PassportID: passport,
	})
}

// frozen is the clock every test here reads. The window a charge lands in must
// be chosen by the test, not by whether the suite happened to run across a UTC
// boundary — a bound tested against the wall clock is a bound that fails on the
// hour (T11).
func frozen(at *time.Time) func() time.Time { return func() time.Time { return *at } }

// aWorkspace is one workspace id, for the tests that need only that they have one.
func aWorkspace() ids.UUID { return ids.New[ids.WorkspaceKind]().UUID }

// aPassport is one Passport id.
func aPassport() ids.UUID { return ids.New[ids.PassportKind]().UUID }

// The bound's arithmetic, end to end: records accumulate ACROSS calls, and the
// threshold is crossed by their sum rather than by any single call. This is
// what "per record, not per call" means operationally — four reads of 30 trip a
// limit of 100 that no one of them approaches.
func TestRecordsAccumulateAcrossCallsUntilTheThresholdIsCrossed(t *testing.T) {
	at := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	meter := NewWithClock(budgettest.Client(t), Limits{Reads: 100}, time.Hour, frozen(&at))
	ctx := meteredCall(t, aWorkspace(), aPassport())

	for range 3 {
		if err := meter.Consume(ctx, Reads, 30); err != nil {
			t.Fatal(err)
		}
	}
	if reading := meter.Read(ctx, Reads); reading.Exceeded || reading.Observed != 90 {
		t.Fatalf("after 90 of 100 records the meter read %+v; it should be under the threshold", reading)
	}

	if err := meter.Consume(ctx, Reads, 30); err != nil {
		t.Fatal(err)
	}

	reading := meter.Read(ctx, Reads)
	if !reading.Exceeded {
		t.Errorf("120 records did not cross a threshold of 100: %+v", reading)
	}
	if reading.Observed != 120 {
		t.Errorf("the meter observed %d records, not the 120 it was handed — the count is what the human is shown", reading.Observed)
	}
}

// A single call over the threshold trips it on its own. This is the evasion
// §2.2 names by hand: "a single search_records returning 5,000 rows trips it".
func TestOneOversizedCallTripsTheThresholdByItself(t *testing.T) {
	at := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	meter := NewWithClock(budgettest.Client(t), Limits{Reads: 100}, time.Hour, frozen(&at))
	ctx := meteredCall(t, aWorkspace(), aPassport())

	if err := meter.Consume(ctx, Reads, 5000); err != nil {
		t.Fatal(err)
	}

	if reading := meter.Read(ctx, Reads); !reading.Exceeded {
		t.Errorf("a 5,000-record answer did not trip a 100-record threshold: %+v", reading)
	}
}

// Two Passports reading the same workspace do not spend each other's budget —
// otherwise one busy agent would step-up every other agent the workspace runs.
func TestOnePassportsReadingDoesNotRefuseAnother(t *testing.T) {
	at := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	meter := NewWithClock(budgettest.Client(t), Limits{Reads: 100}, time.Hour, frozen(&at))
	// ONE workspace, two Passports — the whole point of the test.
	ws := aWorkspace()
	busy := meteredCall(t, ws, aPassport())
	quiet := meteredCall(t, ws, aPassport())

	if err := meter.Consume(busy, Reads, 500); err != nil {
		t.Fatal(err)
	}

	if !meter.Read(busy, Reads).Exceeded {
		t.Error("the busy Passport was not stepped up")
	}
	if meter.Read(quiet, Reads).Exceeded {
		t.Error("a Passport that has read nothing was stepped up by another agent's reading")
	}
}

// The window is FIXED with expiry, so a Passport that crossed the threshold is
// released when the window rolls — asserted by advancing the injected clock
// rather than by waiting for one.
func TestTheWindowRollsOverAndReleasesTheThreshold(t *testing.T) {
	at := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	meter := NewWithClock(budgettest.Client(t), Limits{Reads: 100}, time.Hour, frozen(&at))
	ctx := meteredCall(t, aWorkspace(), aPassport())
	if err := meter.Consume(ctx, Reads, 200); err != nil {
		t.Fatal(err)
	}
	if !meter.Read(ctx, Reads).Exceeded {
		t.Fatal("200 records did not cross a threshold of 100 inside the window")
	}

	at = at.Add(time.Hour)

	reading := meter.Read(ctx, Reads)
	if reading.Exceeded {
		t.Errorf("the next window inherited the previous one's count: %+v", reading)
	}
	if reading.Observed != 0 {
		t.Errorf("a fresh window opened at %d records", reading.Observed)
	}
}

// Confirm-and-continue, against a real counter: an agent that crossed its read
// threshold is refused, its human releases the window it was refused in, and it
// reads again — the whole of BYO-STEP-1, proven end to end rather than by
// arithmetic on a limit.
func TestAReleaseLetsARefusedAgentReadAgainInTheSameWindow(t *testing.T) {
	at := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	meter := NewWithClock(budgettest.Client(t), Limits{Reads: 100}, time.Hour, frozen(&at))
	ws, passport := aWorkspace(), aPassport()
	ctx := meteredCall(t, ws, passport)
	if err := meter.Consume(ctx, Reads, 120); err != nil {
		t.Fatal(err)
	}
	refused := meter.Read(ctx, Reads)
	if !refused.Exceeded {
		t.Fatal("120 records did not cross a threshold of 100")
	}

	applied, err := meter.Release(t.Context(), ws, passport, Reads, refused.Bucket)
	if err != nil || !applied {
		t.Fatalf("releasing the window the agent was refused in: applied=%v err=%v", applied, err)
	}

	after := meter.Read(ctx, Reads)
	if after.Exceeded {
		t.Errorf("the agent is still refused after its human released the window: %+v", after)
	}
	if after.Limit != 200 {
		t.Errorf("one release raised the effective limit to %d, want 200 — one more allowance, not an unbounded one", after.Limit)
	}
	if after.Observed != 120 {
		t.Errorf("the release erased the observed count (now %d); the next approval screen has to show what was actually read", after.Observed)
	}
}

// A release widens ONE window. The agent that spends the released allowance too
// is refused again and needs a second human decision — which is the only bound
// the ladder has, so it must actually hold.
func TestASecondCrossingAfterAReleaseNeedsASecondDecision(t *testing.T) {
	at := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	meter := NewWithClock(budgettest.Client(t), Limits{Reads: 100}, time.Hour, frozen(&at))
	ws, passport := aWorkspace(), aPassport()
	ctx := meteredCall(t, ws, passport)
	if err := meter.Consume(ctx, Reads, 120); err != nil {
		t.Fatal(err)
	}
	if _, err := meter.Release(t.Context(), ws, passport, Reads, meter.Bucket()); err != nil {
		t.Fatal(err)
	}

	if err := meter.Consume(ctx, Reads, 100); err != nil {
		t.Fatal(err)
	}

	if reading := meter.Read(ctx, Reads); !reading.Exceeded {
		t.Errorf("220 records against a once-released 100-record window was admitted: %+v", reading)
	}
}

// A release of one counter does not widen another. Sharing them would mean a
// human confirming "keep reading" also lifted the send ceiling, which is the
// tightest quota on the surface and the one nothing may release at all.
func TestAReleaseWidensOnlyTheCounterItNamed(t *testing.T) {
	at := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	meter := NewWithClock(budgettest.Client(t), Limits{Reads: 10, Writes: 10}, time.Hour, frozen(&at))
	ws, passport := aWorkspace(), aPassport()
	ctx := meteredCall(t, ws, passport)
	for _, c := range []Counter{Reads, Writes} {
		if err := meter.Consume(ctx, c, 12); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := meter.Release(t.Context(), ws, passport, Reads, meter.Bucket()); err != nil {
		t.Fatal(err)
	}

	if meter.Read(ctx, Reads).Exceeded {
		t.Error("the released read counter is still refusing")
	}
	if !meter.Read(ctx, Writes).Exceeded {
		t.Error("releasing reads also released writes; one confirmation answered two questions")
	}
}

// The four governed counters are four independent windows for one Passport. A
// shared one would let a busy reader exhaust its own send allowance — or, worse,
// let a caller spend the loose quota to escape the tight one.
func TestEachCounterIsItsOwnWindowForOnePassport(t *testing.T) {
	at := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	meter := NewWithClock(budgettest.Client(t),
		Limits{Reads: 100, Writes: 10, Egress: 2, Calls: 500}, time.Hour, frozen(&at))
	ctx := meteredCall(t, aWorkspace(), aPassport())

	if err := meter.Consume(ctx, Egress, 3); err != nil {
		t.Fatal(err)
	}

	if !meter.Read(ctx, Egress).Exceeded {
		t.Error("three sends did not cross an egress threshold of two")
	}
	for _, c := range []Counter{Reads, Writes, Calls} {
		if reading := meter.Read(ctx, c); reading.Exceeded || reading.Observed != 0 {
			t.Errorf("spending egress moved %s to %+v", c, reading)
		}
	}
}
