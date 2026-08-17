// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalooa

// The walk and the cursor arithmetic — the part of this unit where a mistake
// loses messages rather than reporting an error.

import (
	"context"
	"testing"
)

// pageOf builds one full page of scripted messages descending from `newest`, so
// a walk that pages has something to page through.
func pageOf(newest int64, ids ...string) []map[string]any {
	page := make([]map[string]any, 0, len(ids))
	for i, id := range ids {
		page = append(page, message(id, newest-int64(i), srcUserToOA, "m"))
	}
	return page
}

// tenFrom builds a full page (the provider's cap), which is what tells the walk
// there may be more under it.
func tenFrom(newest int64) []map[string]any {
	ids := make([]string, maxChatPage)
	for i := range ids {
		ids[i] = string(rune('a'+i)) + "-" + string(rune('0'+newest%10))
	}
	return pageOf(newest, ids...)
}

// A page shorter than the cap is the end of the feed, which is how this provider
// says so: an offset past the end answers success with no rows rather than an
// error.
func TestAShortPageClosesTheWalk(t *testing.T) {
	fake := newZaloFake(t)
	fake.chatPages = [][]map[string]any{pageOf(1000, "a", "b", "c")}

	result, err := walkChats(t.Context(), fake.client("t"), walkSpec{budget: maxPagesPerPoll})
	if err != nil {
		t.Fatalf("walkChats: %v", err)
	}
	if !result.closed {
		t.Fatal("the walk did not close on a short page, so the cursor would never advance past this region")
	}
	if len(result.items) != 3 {
		t.Fatalf("collected %d messages, want 3", len(result.items))
	}
	if result.oldest != 998 {
		t.Fatalf("oldest = %d, want 998", result.oldest)
	}
}

// The stop boundary is INCLUSIVE: a message whose timestamp equals the floor is
// collected and decided again. Zalo timestamps are milliseconds and two messages
// can share one, so an exclusive boundary would drop the second of any such pair
// forever — and re-deciding costs a deduplicated replay.
func TestTheStopBoundaryReDecidesTheMessageSittingOnItRatherThanSkippingIt(t *testing.T) {
	fake := newZaloFake(t)
	fake.chatPages = [][]map[string]any{{
		message("newer", 1000, srcUserToOA, "m"),
		message("on-the-floor", 900, srcUserToOA, "m"),
		message("also-on-the-floor", 900, srcUserToOA, "m"),
		message("older", 899, srcUserToOA, "m"),
	}}

	result, err := walkChats(t.Context(), fake.client("t"), walkSpec{stopBelow: 900, budget: 2})
	if err != nil {
		t.Fatalf("walkChats: %v", err)
	}
	if len(result.items) != 3 {
		t.Fatalf("collected %d messages, want 3 — both messages sharing the boundary millisecond must be collected", len(result.items))
	}
	if !result.closed {
		t.Fatal("the walk did not close on reaching the stop point")
	}
}

// A walk that spends its budget does NOT close, which is what tells the cursor to
// leave an unread region behind rather than advancing the floor over it.
func TestAWalkThatSpendsItsBudgetLeavesTheRegionUnderItOpen(t *testing.T) {
	fake := newZaloFake(t)
	fake.chatPages = [][]map[string]any{tenFrom(2000), tenFrom(1000), tenFrom(500)}

	result, err := walkChats(t.Context(), fake.client("t"), walkSpec{budget: 2})
	if err != nil {
		t.Fatalf("walkChats: %v", err)
	}
	if result.closed {
		t.Fatal("the walk reported itself closed after spending its budget, which would advance the floor over messages nothing read")
	}
	if result.stoppedAtPage != 1 {
		t.Fatalf("stoppedAtPage = %d, want 1", result.stoppedAtPage)
	}
	if len(result.items) != 2*maxChatPage {
		t.Fatalf("collected %d messages, want %d", len(result.items), 2*maxChatPage)
	}
}

// A backfill pages THROUGH the region above the gap without collecting it,
// because the provider offers no way to seek to a timestamp.
func TestABackfillPagesPastWhatIsAlreadyDecidedWithoutCollectingIt(t *testing.T) {
	fake := newZaloFake(t)
	fake.chatPages = [][]map[string]any{{
		message("decided", 2000, srcUserToOA, "m"),
		message("unread", 1500, srcUserToOA, "m"),
		message("under the floor", 500, srcUserToOA, "m"),
	}}

	result, err := walkChats(t.Context(), fake.client("t"), walkSpec{
		stopBelow: 1000, skipAtOrAbove: 1800, budget: 2,
	})
	if err != nil {
		t.Fatalf("walkChats: %v", err)
	}
	if len(result.items) != 1 || result.items[0].MessageID != "unread" {
		t.Fatalf("collected %+v, want only the message inside the unread region", result.items)
	}
}

// A first poll takes the page it read and keeps nothing under it. Connecting an
// account brings the CRM what arrives from now on; importing an account's whole
// history is a decision with a cost, and not one an authorization click makes.
func TestAFirstPollTakesTheNewestPageAndOpensNoBacklog(t *testing.T) {
	after := afterForward(cursor{}, 1000, walkResult{closed: false, oldest: 900, stoppedAtPage: 0})
	if after.floor != 1000 {
		t.Fatalf("floor = %d, want 1000", after.floor)
	}
	if after.gap != 0 || after.top != 0 || after.offset != 0 {
		t.Fatalf("a first poll left a backlog behind it: %+v", after)
	}
}

// A truncated forward walk leaves the FLOOR where it was and records what it did
// decide as `top`, so the next tick reads new messages first rather than
// re-walking the burst.
func TestATruncatedForwardWalkHoldsTheFloorAndRemembersWhatItDecided(t *testing.T) {
	before := cursor{floor: 100}
	after := afterForward(before, 2000, walkResult{closed: false, oldest: 1500, stoppedAtPage: 3})

	if after.floor != 100 {
		t.Fatalf("floor = %d, want it held at 100 — moving it would strand every message under the gap", after.floor)
	}
	if after.top != 2000 {
		t.Fatalf("top = %d, want 2000", after.top)
	}
	if after.gap != 1500 {
		t.Fatalf("gap = %d, want 1500", after.gap)
	}
	if after.offset != 3*maxChatPage {
		t.Fatalf("offset = %d, want the page the walk stopped at", after.offset)
	}
}

// The ordinary tick: a closed walk with no backlog under it moves the floor and
// clears the rest.
func TestAClosedForwardWalkWithNoBacklogAdvancesTheFloor(t *testing.T) {
	after := afterForward(cursor{floor: 100}, 2000, walkResult{closed: true, oldest: 150})
	if after.floor != 2000 || after.top != 0 || after.gap != 0 {
		t.Fatalf("cursor = %+v, want the floor at 2000 and nothing else set", after)
	}
}

// Closing the gap is what finally moves the floor: the region between the floor
// and the top has been read in full, so the two collapse into one number.
func TestClosingTheBacklogCollapsesTheCursorBackToOneNumber(t *testing.T) {
	after := afterBackfill(cursor{floor: 100, gap: 1500, top: 2000, offset: 30}, walkResult{closed: true})
	if after.floor != 2000 {
		t.Fatalf("floor = %d, want the top it had already decided to", after.floor)
	}
	if after.gap != 0 || after.top != 0 || after.offset != 0 {
		t.Fatalf("cursor = %+v, want the backlog cleared", after)
	}
}

// A backfill that runs out of budget just moves the gap down, and the next tick
// resumes under it — with the newest messages still read first.
func TestATruncatedBacklogWalkMovesTheGapDownAndKeepsTheFloor(t *testing.T) {
	after := afterBackfill(cursor{floor: 100, gap: 1500, top: 2000, offset: 30},
		walkResult{closed: false, oldest: 800, stoppedAtPage: 6})
	if after.floor != 100 {
		t.Fatalf("floor = %d, want it held", after.floor)
	}
	if after.gap != 800 {
		t.Fatalf("gap = %d, want 800", after.gap)
	}
	if after.offset != 6*maxChatPage {
		t.Fatalf("offset = %d, want the page it stopped at", after.offset)
	}
}

// The resume is shifted by what has arrived since AND stepped one page shallower.
// Landing too deep skips messages permanently; landing too shallow re-reads a
// page that is then discarded, which costs one request.
func TestTheBacklogResumesOnePageShallowerThanTheArithmeticSays(t *testing.T) {
	at := cursor{gap: 800, offset: 60}
	if got := resumePage(at, 20); got != (60+20)/maxChatPage-1 {
		t.Fatalf("resumePage = %d, want the arithmetic minus one page", got)
	}
	// And it never goes below the newest page, which does not exist.
	if got := resumePage(cursor{offset: 0}, 0); got != 0 {
		t.Fatalf("resumePage = %d on a cursor at the top, want 0", got)
	}
}

// The forward walk stops at `top` while a backlog is open, so a tick does not
// re-walk what it decided last time on its way to what is new.
func TestTheForwardWalkStopsAtTheTopWhileABacklogIsOpen(t *testing.T) {
	if got := (cursor{floor: 100, gap: 800, top: 2000}).forwardFrom(); got != 2000 {
		t.Fatalf("forwardFrom = %d, want the top", got)
	}
	if got := (cursor{floor: 100}).forwardFrom(); got != 100 {
		t.Fatalf("forwardFrom = %d, want the floor when there is no backlog", got)
	}
}

// A provider that refuses stops the walk with nothing collected, so no cursor can
// be computed from a partial read.
func TestAProviderRefusalStopsTheWalkWithNothingCollected(t *testing.T) {
	fake := newZaloFake(t)
	fake.errorCode = codeTokenExpired
	fake.chatPages = [][]map[string]any{pageOf(1000, "a")}

	result, err := walkChats(context.Background(), fake.client("t"), walkSpec{budget: 2})
	if err == nil {
		t.Fatal("the walk returned no error against a provider that refused every page")
	}
	if len(result.items) != 0 {
		t.Fatalf("collected %d messages from a refused walk", len(result.items))
	}
}
