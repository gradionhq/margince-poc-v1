//go:build integration

package compose

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
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// schedulerPerms is repPerms plus the activity grant the booking write
// needs; row scope stays team.
var schedulerPerms = principal.Permissions{
	RoleKeys: []string{"rep"},
	Objects: map[string]principal.ObjectGrant{
		"person":   {Create: true, Read: true, Update: true},
		"activity": {Create: true, Read: true, Update: true},
	},
	RowScope: principal.RowScopeTeam,
}

func TestBookingAnotherHostNeedsUnboundedScope(t *testing.T) {
	e := setupAuthz(t)
	slotStart := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)

	rep1 := e.as(e.rep1, []ids.UUID{e.team1}, schedulerPerms)
	if _, err := e.activities.BookMeeting(rep1, activities.BookMeetingInput{
		Host: e.rep1, Start: slotStart, End: slotStart.Add(time.Hour),
	}); err != nil {
		t.Fatalf("self-booking: %v", err)
	}
	if _, err := e.activities.BookMeeting(rep1, activities.BookMeetingInput{
		Host: e.rep2, Start: slotStart, End: slotStart.Add(time.Hour),
	}); !errors.Is(err, apperrors.ErrPermissionDenied) {
		t.Fatalf("booking rep2's calendar as rep1 → %v, want ErrPermissionDenied", err)
	}
	// An unbounded scope may book on behalf — and hits the conflict
	// guard like anyone else.
	admin := e.admin()
	if _, err := e.activities.BookMeeting(admin, activities.BookMeetingInput{
		Host: e.rep2, Start: slotStart, End: slotStart.Add(time.Hour),
	}); err != nil {
		t.Fatalf("admin booking for rep2: %v", err)
	}
	var slotTaken *activities.SlotTakenError
	if _, err := e.activities.BookMeeting(rep1, activities.BookMeetingInput{
		Host: e.rep1, Start: slotStart, End: slotStart.Add(time.Hour),
	}); !errors.As(err, &slotTaken) {
		t.Fatalf("double self-booking → %v, want SlotTakenError", err)
	}
}

func TestAvailabilityBusyReadHonorsRowScope(t *testing.T) {
	e := setupAuthz(t)
	slotStart := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)

	// rep1's meeting is linked to a person OWNED by rep1 — visible to
	// the team, hidden from the other team.
	target := e.seedPerson(t, "Scoped Client", &e.rep1)
	rep1 := e.as(e.rep1, []ids.UUID{e.team1}, schedulerPerms)
	if _, err := e.activities.BookMeeting(rep1, activities.BookMeetingInput{
		Host: e.rep1, Start: slotStart, End: slotStart.Add(time.Hour),
		Links: []activities.ActivityLinkInput{{EntityType: "person", EntityID: target}},
	}); err != nil {
		t.Fatalf("booking: %v", err)
	}

	windowFrom, windowTo := slotStart.Add(-2*time.Hour), slotStart.Add(6*time.Hour)
	proposes := func(ctx context.Context) bool {
		t.Helper()
		slots, err := e.activities.Availability(ctx, e.rep1, windowFrom, windowTo, time.Hour)
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
	teammate := e.as(e.rep2, []ids.UUID{e.team1}, schedulerPerms)
	if proposes(teammate) {
		t.Fatal("teammate still sees the booked slot as free")
	}
	stranger := e.as(e.rep3, []ids.UUID{e.team2}, schedulerPerms)
	if !proposes(stranger) {
		t.Fatal("out-of-scope caller can see the busy block — free/busy leaks the hidden meeting")
	}
}
