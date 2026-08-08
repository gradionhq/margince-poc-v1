// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package readmeter

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

// meteredCall builds a context for one Passport, and the clock the meter reads
// its window from, so a test can advance time rather than sleep.
func meteredCall(t *testing.T, passport ids.UUID) context.Context {
	t.Helper()
	ctx := principal.WithWorkspaceID(t.Context(), ids.New[ids.WorkspaceKind]().UUID)
	return principal.WithActor(ctx, principal.Principal{
		Type: principal.PrincipalAgent, ID: "agent:reader", PassportID: passport,
	})
}

// The bound's arithmetic, end to end: records accumulate ACROSS calls, and the
// threshold is crossed by their sum rather than by any single call. This is
// what "per record, not per call" means operationally — four reads of 30 trip a
// limit of 100 that no one of them approaches.
func TestRecordsAccumulateAcrossCallsUntilTheThresholdIsCrossed(t *testing.T) {
	meter := New(budgettest.Client(t), 100, time.Hour)
	ctx := meteredCall(t, ids.New[ids.PassportKind]().UUID)

	for range 3 {
		if err := meter.Consume(ctx, 30); err != nil {
			t.Fatal(err)
		}
	}
	if reading := meter.Read(ctx); reading.Exceeded || reading.Observed != 90 {
		t.Fatalf("after 90 of 100 records the meter read %+v; it should be under the threshold", reading)
	}

	if err := meter.Consume(ctx, 30); err != nil {
		t.Fatal(err)
	}

	reading := meter.Read(ctx)
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
	meter := New(budgettest.Client(t), 100, time.Hour)
	ctx := meteredCall(t, ids.New[ids.PassportKind]().UUID)

	if err := meter.Consume(ctx, 5000); err != nil {
		t.Fatal(err)
	}

	if reading := meter.Read(ctx); !reading.Exceeded {
		t.Errorf("a 5,000-record answer did not trip a 100-record threshold: %+v", reading)
	}
}

// An approved step-up releases the agent WITHOUT erasing what it has read: the
// count stays, the limit moves. A human asked to confirm a second time must
// see the true running total, not a total reset by the first confirmation.
func TestAStepUpGrantRaisesTheLimitAndLeavesTheCountIntact(t *testing.T) {
	meter := New(budgettest.Client(t), 100, time.Hour)
	ctx := meteredCall(t, ids.New[ids.PassportKind]().UUID)
	if err := meter.Consume(ctx, 120); err != nil {
		t.Fatal(err)
	}

	if err := meter.Grant(ctx, 100); err != nil {
		t.Fatal(err)
	}

	reading := meter.Read(ctx)
	if reading.Exceeded {
		t.Errorf("an approved step-up did not release the read: %+v", reading)
	}
	if reading.Observed != 120 {
		t.Errorf("the step-up reset the count to %d; the audit answer to \"how many records did it see\" is gone", reading.Observed)
	}
	if reading.Limit != 200 {
		t.Errorf("the granted limit is %d, not the 100 default plus the 100 granted", reading.Limit)
	}
}

// Two Passports reading the same workspace do not spend each other's budget —
// otherwise one busy agent would step-up every other agent the workspace runs.
func TestOnePassportsReadingDoesNotRefuseAnother(t *testing.T) {
	client := budgettest.Client(t)
	meter := New(client, 100, time.Hour)
	busy := meteredCall(t, ids.New[ids.PassportKind]().UUID)
	quiet := meteredCall(t, ids.New[ids.PassportKind]().UUID)

	if err := meter.Consume(busy, 500); err != nil {
		t.Fatal(err)
	}

	if !meter.Read(busy).Exceeded {
		t.Error("the busy Passport was not stepped up")
	}
	if meter.Read(quiet).Exceeded {
		t.Error("a Passport that has read nothing was stepped up by another agent's reading")
	}
}

// The window is FIXED with expiry, so a Passport that crossed the threshold is
// released when the window rolls — asserted by advancing the injected clock
// rather than by waiting for one.
func TestTheWindowRollsOverAndReleasesTheThreshold(t *testing.T) {
	now := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	meter := NewWithClock(budgettest.Client(t), 100, time.Hour, func() time.Time { return now })
	ctx := meteredCall(t, ids.New[ids.PassportKind]().UUID)
	if err := meter.Consume(ctx, 200); err != nil {
		t.Fatal(err)
	}
	if !meter.Read(ctx).Exceeded {
		t.Fatal("200 records did not cross a threshold of 100 inside the window")
	}

	now = now.Add(time.Hour)

	reading := meter.Read(ctx)
	if reading.Exceeded {
		t.Errorf("the next window inherited the previous one's count: %+v", reading)
	}
	if reading.Observed != 0 {
		t.Errorf("a fresh window opened at %d records", reading.Observed)
	}
}

// A step-up granted in one window does not release the next one. A human
// confirming "read 200 more today" has not confirmed tomorrow.
func TestAStepUpGrantDoesNotSurviveIntoTheNextWindow(t *testing.T) {
	now := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	meter := NewWithClock(budgettest.Client(t), 100, time.Hour, func() time.Time { return now })
	ctx := meteredCall(t, ids.New[ids.PassportKind]().UUID)
	if err := meter.Grant(ctx, 1000); err != nil {
		t.Fatal(err)
	}

	now = now.Add(time.Hour)
	if err := meter.Consume(ctx, 150); err != nil {
		t.Fatal(err)
	}

	if reading := meter.Read(ctx); !reading.Exceeded {
		t.Errorf("the previous window's step-up grant released this one: %+v", reading)
	}
}
