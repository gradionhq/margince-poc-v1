// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package activities

// What the slot walk says about its own answer, and how it refuses arguments it
// cannot serve. Both were reported as defects on the MCP surface: a capped list
// arrived with nothing marking it, and the from/to refusal rendered garbled.
//
// freeSlots is pure — window in, slots out — so these need no database and none
// of them is an integration test.

import (
	"errors"
	"testing"
	"time"

	"github.com/gradionhq/margince/backend/internal/platform/httperr"
)

// A Monday, so the business-hours filter admits the whole window.
func monday(hour int) time.Time {
	return time.Date(2026, 8, 3, hour, 0, 0, 0, time.UTC)
}

func TestASlotWalkStoppedByItsCapSaysSo(t *testing.T) {
	// A full business day at the minimum slot length offers more candidates than
	// the cap admits, so the answer is a prefix. A model handed a prefix with no
	// marker tells a rep there is no later opening — which is why truncated
	// exists rather than being left for a caller to infer from the count.
	free, truncated := freeSlots(monday(9), monday(17), minSlotDuration, nil)
	if len(free) != maxProposedSlots {
		t.Fatalf("a full day at %v yielded %d slots, want the cap %d",
			minSlotDuration, len(free), maxProposedSlots)
	}
	if !truncated {
		t.Error("the walk stopped at its cap with window left to scan and reported truncated=false — " +
			"a bounded answer presented as the whole truth")
	}
}

func TestASlotWalkThatFinishedTheWindowIsNotTruncated(t *testing.T) {
	// The mirror case: exactly two slots fit, the walk runs out of window rather
	// than out of budget, so nothing was withheld. A flag stuck at true would be
	// as misleading as one stuck at false.
	free, truncated := freeSlots(monday(9), monday(10), time.Hour/2, nil)
	if len(free) != 2 {
		t.Fatalf("a one-hour window at 30m yielded %d slots, want 2", len(free))
	}
	if truncated {
		t.Error("the walk consumed the whole window but reported truncated=true")
	}
}

func TestNoFreeSlotIsAnEmptyArrayNotNull(t *testing.T) {
	// "Booked solid" is a real answer. nil marshals to null, which a model reads
	// as "unknown" and hedges over — and it disagrees on type with the array the
	// contract declares. Normalized in the walk so all three transports inherit
	// it; the MCP adapter is the one that used to emit the null.
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
	// The reported defect: these were RequiredFieldError values carrying prose in
	// Field, so the MCP dispatcher rendered `validation_error to (must follow
	// from)=required` and REST put the same parenthetical in
	// details.errors[].field. Both the name and the code were wrong — the value
	// was supplied, so nothing about it was `required`.
	for _, tc := range []struct {
		name  string
		err   error
		field string
		code  string
	}{
		{"to before from", errAvailabilityToNotAfterFrom, "to", "invalid_range"},
		{"window too wide", errAvailabilityWindowTooWide, "to", "window_too_wide"},
		{"duration out of range", errAvailabilityDurationOutOfRange, "duration_minutes", "out_of_range"},
		{"booking end before start", errBookingEndNotAfterStart, "end", "invalid_range"},
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
				t.Errorf("code = %q, want %q — `required` is false for a value that was supplied", got.Code, tc.code)
			}
			// The message is where the explanation belongs, and it has to
			// actually carry one: a field and a code alone do not say what is
			// wrong with the pair.
			if got.Message == "" {
				t.Error("the refusal carries no message")
			}
		})
	}
}

func TestABookingThatEndsBeforeItStartsIsRefusedWithTheSameErrorEverywhere(t *testing.T) {
	// book_meeting's StageInfo pre-empts this so no approval is minted for a
	// booking that cannot happen; the store is what makes it true on the paths
	// StageInfo does not run (REST, and redemption). One value, so the two cannot
	// drift onto different answers for the same condition.
	var sched *SchedulingArgumentError
	if !errors.As(error(errBookingEndNotAfterStart), &sched) {
		t.Fatal("the booking refusal is not a SchedulingArgumentError")
	}
	if sched.Field != "end" {
		t.Errorf("the booking refusal names %q, want end", sched.Field)
	}
}
