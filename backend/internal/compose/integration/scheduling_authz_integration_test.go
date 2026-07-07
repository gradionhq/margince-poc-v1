// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

//go:build integration

package integration

// Scheduling is calendar access, so it carries the row-scope posture:
// booking another host's calendar needs an unbounded scope, and the
// availability busy-read shows a caller only the meetings their
// timeline would — a stranger's calendar never leaks through free/busy.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/modules/activities"
	"github.com/gradionhq/margince/backend/internal/shared/apperrors"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
)

func TestBookingAnotherHostNeedsUnboundedScope(t *testing.T) {
	e := Setup(t)
	slotStart := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)

	rep1 := e.As(e.Rep1, []ids.UUID{e.Team1}, SchedulerPerms)
	if _, err := e.Activities.BookMeeting(rep1, activities.BookMeetingInput{
		Host: ids.From[ids.UserKind](e.Rep1), Start: slotStart, End: slotStart.Add(time.Hour),
	}); err != nil {
		t.Fatalf("self-booking: %v", err)
	}
	if _, err := e.Activities.BookMeeting(rep1, activities.BookMeetingInput{
		Host: ids.From[ids.UserKind](e.Rep2), Start: slotStart, End: slotStart.Add(time.Hour),
	}); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("booking rep2's calendar as rep1 → %v, want ErrPermissionDenied", err)
	}
	// An unbounded scope may book on behalf — and hits the conflict
	// guard like anyone else.
	admin := e.Admin()
	if _, err := e.Activities.BookMeeting(admin, activities.BookMeetingInput{
		Host: ids.From[ids.UserKind](e.Rep2), Start: slotStart, End: slotStart.Add(time.Hour),
	}); err != nil {
		t.Fatalf("admin booking for rep2: %v", err)
	}
	var slotTaken *activities.SlotTakenError
	if _, err := e.Activities.BookMeeting(rep1, activities.BookMeetingInput{
		Host: ids.From[ids.UserKind](e.Rep1), Start: slotStart, End: slotStart.Add(time.Hour),
	}); !errors.As(err, &slotTaken) {
		t.Fatalf("double self-booking → %v, want SlotTakenError", err)
	}
}

func TestAvailabilityBusyReadHonorsRowScope(t *testing.T) {
	e := Setup(t)
	slotStart := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)

	// rep1's meeting is linked to a person OWNED by rep1 — visible to
	// the team, hidden from the other team.
	target := e.SeedPerson(t, "Scoped Client", &e.Rep1)
	rep1 := e.As(e.Rep1, []ids.UUID{e.Team1}, SchedulerPerms)
	if _, err := e.Activities.BookMeeting(rep1, activities.BookMeetingInput{
		Host: ids.From[ids.UserKind](e.Rep1), Start: slotStart, End: slotStart.Add(time.Hour),
		Links: []activities.ActivityLinkInput{{EntityType: "person", EntityID: target}},
	}); err != nil {
		t.Fatalf("booking: %v", err)
	}

	windowFrom, windowTo := slotStart.Add(-2*time.Hour), slotStart.Add(6*time.Hour)
	proposes := func(ctx context.Context) bool {
		t.Helper()
		slots, err := e.Activities.Availability(ctx, ids.From[ids.UserKind](e.Rep1), windowFrom, windowTo, time.Hour)
		if err != nil {
			t.Fatalf("availability: %v", err)
		}
		for _, s := range slots {
			if s.Start.Equal(slotStart) {
				return true
			}
		}
		return false
	}

	// A teammate sees the block; a rep from the other team sees rep1's
	// calendar as free at that slot — the meeting is outside their row
	// scope and must not leak through free/busy.
	teammate := e.As(e.Rep2, []ids.UUID{e.Team1}, SchedulerPerms)
	if proposes(teammate) {
		t.Fatal("teammate still sees the booked slot as free")
	}
	stranger := e.As(e.Rep3, []ids.UUID{e.Team2}, SchedulerPerms)
	if !proposes(stranger) {
		t.Fatal("out-of-scope caller can see the busy block — free/busy leaks the hidden meeting")
	}
}
