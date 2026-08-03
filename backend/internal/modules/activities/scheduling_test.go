// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// What the slot walk says about its own answer, and how scheduling refuses
// arguments no calendar can serve.
//
// Two properties carry the weight. A capped answer must admit it is capped —
// and must not claim it when nothing was withheld, which is the harder half.
// And a refusal must name a real argument under a code that is true of it.
//
// freeSlots is pure (window in, slots out) and the argument refusals land before
// any query, so none of this needs a database.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/platform/httperr"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/ids"
	"github.com/gradionhq/margince/backend/internal/shared/kernel/principal"
)

// A Monday, so the business-hours filter admits the whole window.
func monday(hour int) time.Time {
	return time.Date(2026, 8, 3, hour, 0, 0, 0, time.UTC)
}

func TestASlotWalkStoppedByItsCapSaysSo(t *testing.T) {
	// A full business day at the minimum slot length offers far more candidates
	// than the cap admits, so the answer is a prefix. A model handed a prefix
	// with no marker tells a rep there is no later opening.
	free, truncated := freeSlots(monday(9), monday(17), minSlotDuration, nil)
	if len(free) != maxProposedSlots {
		t.Fatalf("a full day at %v yielded %d slots, want the cap %d",
			minSlotDuration, len(free), maxProposedSlots)
	}
	if !truncated {
		t.Error("more free slots exist past the cap and the answer reported truncated=false — " +
			"a bounded answer presented as the whole truth")
	}
}

func TestExactlyTheCapWithNothingLeftIsNotTruncated(t *testing.T) {
	// The boundary that makes truncated mean anything. A window holding exactly
	// maxProposedSlots free slots withheld nothing, so claiming truncation there
	// sends a caller hunting for slots that do not exist — which is why the walk
	// establishes truncation by FINDING one more rather than by hitting the cap.
	//
	// The window is DERIVED, not guessed: business hours and weekends mean an
	// arithmetic guess (cap × duration) lands nowhere near the cap, and such a
	// window would pass this test while proving nothing about the boundary.
	from := monday(9)
	wide, wideTruncated := freeSlots(from, from.Add(14*24*time.Hour), time.Hour, nil)
	if len(wide) != maxProposedSlots || !wideTruncated {
		t.Fatalf("a fortnight yielded %d slots (truncated=%v); this test needs a window that "+
			"genuinely overruns the cap %d", len(wide), wideTruncated, maxProposedSlots)
	}
	// End the window exactly where the last admitted slot ends: the cap is now
	// reached with nothing left to find.
	exact, truncated := freeSlots(from, wide[len(wide)-1].End, time.Hour, nil)
	if len(exact) != maxProposedSlots {
		t.Fatalf("the derived window yielded %d slots, want exactly the cap %d", len(exact), maxProposedSlots)
	}
	if truncated {
		t.Errorf("a window holding exactly %d free slots reported truncated=true; nothing was withheld",
			len(exact))
	}
}

func TestASlotWalkThatFinishedTheWindowIsNotTruncated(t *testing.T) {
	free, truncated := freeSlots(monday(9), monday(10), time.Hour/2, nil)
	if len(free) != 2 {
		t.Fatalf("a one-hour window at 30m yielded %d slots, want 2", len(free))
	}
	if truncated {
		t.Error("the walk consumed the whole window but reported truncated=true")
	}
}

func TestNoFreeSlotIsAnEmptyArrayNotNull(t *testing.T) {
	// "Booked solid" is a real answer, and it has to arrive shaped like the array
	// the contract declares: nil marshals to null, which a model reads as
	// "unknown" rather than "none free".
	free, truncated := freeSlots(monday(3), monday(4), time.Hour, nil)
	if free == nil {
		t.Error("an empty result is nil, which reaches the wire as null")
	}
	if len(free) != 0 {
		t.Errorf("a window outside business hours yielded %d slots, want none", len(free))
	}
	if truncated {
		t.Error("an empty answer claims truncation")
	}
}

func TestSchedulingRefusalsNameARealArgumentAndAnHonestCode(t *testing.T) {
	// Field is what both surfaces publish as the MACHINE field name, so it has to
	// be one — and the code has to be true of the condition. A value that was
	// supplied and is merely inconsistent is not `required`, and a caller acting
	// on that code would re-send a field it had already sent.
	for _, tc := range []struct {
		name  string
		err   error
		field string
		code  string
	}{
		{"to before from", errAvailabilityToNotAfterFrom, "to", "invalid_date_range"},
		{"window too wide", errAvailabilityWindowTooWide, "to", "window_too_wide"},
		{"duration out of range", errAvailabilityDurationOutOfRange, "duration_minutes", "out_of_range"},
		{"booking end before start", errBookingEndNotAfterStart, "end", "invalid_date_range"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fault, ok := httperr.Classify(tc.err)
			if !ok {
				t.Fatalf("%v is classified by nothing, so the MCP surface reports it as an internal fault", tc.err)
			}
			if len(fault.Fields) != 1 {
				t.Fatalf("fault carries %d field entries, want exactly 1: %#v", len(fault.Fields), fault.Fields)
			}
			got := fault.Fields[0]
			if got.Field != tc.field {
				t.Errorf("field = %q, want %q", got.Field, tc.field)
			}
			if got.Code != tc.code {
				t.Errorf("code = %q, want %q", got.Code, tc.code)
			}
			// The explanation belongs in the message, and there has to be one: a
			// field and a code alone do not say what is wrong with the pair.
			if got.Message == "" {
				t.Error("the refusal carries no message")
			}
		})
	}
}

func TestABookingRefusesAnEndThatDoesNotFollowItsStart(t *testing.T) {
	// Driven through BookMeeting rather than read off the error value: a test that
	// only inspects a package variable passes with the check deleted. The store is
	// the path that has to hold — book_meeting's StageInfo pre-empts the same
	// condition so no approval is minted for an unbookable slot, but REST and an
	// approved retry both arrive here.
	//
	// A zero-length booking is refused as well as a backwards one. "Ends when it
	// starts" is the case a `Before` check would wave through.
	host := ids.NewV7()
	store := &Store{}
	start := monday(10)
	for _, end := range []time.Time{start, start.Add(-time.Hour)} {
		_, err := store.BookMeeting(bookingActorCtx(host), BookMeetingInput{
			Host: ids.From[ids.UserKind](host), Start: start, End: end,
		})
		var sched *SchedulingArgumentError
		if !errors.As(err, &sched) {
			t.Fatalf("start=%s end=%s → %T (%v), want *SchedulingArgumentError",
				start.Format(time.RFC3339), end.Format(time.RFC3339), err, err)
		}
		if sched.Field != "end" {
			t.Errorf("the refusal names %q, want end — that is the argument to move", sched.Field)
		}
	}
}

// bookingActorCtx is a human who may create activities, booking their OWN
// calendar. Both halves are authority BookMeeting settles before it looks at the
// slot — without them the refusal under test never runs, and the assertion would
// silently be about permissions or delegation instead.
func bookingActorCtx(userID ids.UUID) context.Context {
	return principal.WithActor(context.Background(), principal.Principal{
		Type:     principal.PrincipalHuman,
		ID:       "human:" + userID.String(),
		UserID:   userID,
		SeatType: principal.SeatFull,
		Permissions: principal.Permissions{
			RoleKeys: []string{"rep"},
			Objects:  map[string]principal.ObjectGrant{"activity": {Read: true, Create: true}},
			RowScope: principal.RowScopeOwn,
		},
	})
}
