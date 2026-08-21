// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package relayprobe

// The cursor over MANY ticks, which is the level the two defects that reached
// review lived at.
//
// Each step was tested on its own and each step was right; what nobody drove
// was the protocol they compose into. A first poll recorded a gap and then
// walked the member's entire past one page at a time — the opposite of the
// comment above it — and a truncated walk threw away everything it had just
// decided, so a busy account's floor crawled while its newest messages waited
// behind a backlog. Both are only visible by running tick after tick and
// watching where the three numbers go.

import (
	"context"
	"testing"
)

// tickOnce runs the cursor arithmetic for one tick against a feed, without a
// provider or a database: the walk is the real one, the landing is "everything
// fetched was decided about", and what comes back is the cursor a real tick
// would have written.
func tickOnce(t *testing.T, provider *fakeProvider, at cursor) cursor {
	t.Helper()
	api := provider.start(t)
	budget := maxPagesPerPoll
	if at.firstPoll() {
		budget = firstPollPages
	}
	forward, err := walkInbox(context.Background(), api, at.forwardFrom(), 0, budget)
	if err != nil {
		t.Fatalf("the forward walk: %v", err)
	}
	after := afterForward(at, highestOf(forward), forward)
	if spent := len(forward.items)/maxPageSize + 1; after.unread() && spent < budget {
		backfill, err := walkInbox(context.Background(), api, after.floor, after.gap, budget-spent)
		if err != nil {
			t.Fatalf("the backfill walk: %v", err)
		}
		after = afterBackfill(after, backfill)
	}
	return after
}

// highestOf is what a tick decided about: every id it fetched, since a poll
// decides about what it filters exactly as much as about what it lands.
func highestOf(result walkResult) int64 {
	var highest int64
	for _, item := range result.items {
		if item.ID > highest {
			highest = item.ID
		}
	}
	return highest
}

// feedOf builds a descending feed of ids 1..n, newest first.
func feedOf(n int64) []inboxItem {
	items := make([]inboxItem, 0, n)
	for id := n; id >= 1; id-- {
		items = append(items, dm(id))
	}
	return items
}

// Connecting brings what arrives from now on. The FIRST tick reads one page and
// leaves nothing behind it — no gap, nothing to walk back through — which is
// what makes the sentence in pollConnection true rather than merely written
// down. An earlier version recorded a gap here, and every later tick walked the
// member's whole past instead of reading anything new.
func TestAFirstPollImportsNoHistory(t *testing.T) {
	provider := &fakeProvider{items: feedOf(500), pageSize: maxPageSize}

	at := tickOnce(t, provider, cursor{})
	if at.unread() {
		t.Fatalf("the first poll left a backlog to walk (gap=%d) — connecting imported the member's history", at.gap)
	}
	if at.top != 0 {
		t.Errorf("top = %d, want 0 — there is no region above an unread one, because there is no unread one", at.top)
	}
	if at.floor != 500 {
		t.Errorf("floor = %d, want the newest id read (500)", at.floor)
	}
	// And the next tick reads only what has arrived since.
	provider.items = append(feedOf(505)[:5], provider.items...)
	next := tickOnce(t, provider, at)
	if next.floor != 505 || next.unread() {
		t.Errorf("cursor = %+v, want the floor at the newest id and no backlog", next)
	}
}

// A burst larger than one tick's budget leaves a backlog — and the tick still
// records what it decided about, so the NEXT tick reads what has newly arrived
// rather than starting again from the bottom of the burst.
func TestATruncatedWalkKeepsWhatItDecidedAndRemembersTheRest(t *testing.T) {
	// A connection that has been polling, and then 1,000 messages arrive.
	established := cursor{floor: 10}
	provider := &fakeProvider{items: feedOf(1010), pageSize: maxPageSize}

	at := tickOnce(t, provider, established)
	switch {
	case at.floor != 10:
		t.Errorf("floor = %d, want it left at 10 — advancing over an unread region strands it", at.floor)
	case at.top == 0:
		t.Error("top = 0 — the tick threw away everything it had just decided, so the next one re-walks it")
	case !at.unread():
		t.Error("no gap recorded — the region under the burst would never be read")
	}
}

// The whole protocol: a backlog converges, and it converges in a number of
// ticks proportional to its size rather than to its square.
//
// This is the test the two reviews asked for. The old arithmetic passed every
// single-step test and failed here: each cycle advanced the floor by at most
// one tick's worth, then re-walked the whole region from the top again.
func TestABacklogConvergesAndTheNewestMessagesDoNotWaitForIt(t *testing.T) {
	const backlog = 1000
	provider := &fakeProvider{items: feedOf(backlog), pageSize: maxPageSize}
	// An established connection with a floor near the bottom, so the whole feed
	// above it is unread.
	at := cursor{floor: 5}

	ticks := 0
	for at.unread() || ticks == 0 {
		at = tickOnce(t, provider, at)
		ticks++
		if ticks > 20 {
			t.Fatalf("the backlog did not converge in %d ticks; cursor = %+v", ticks, at)
		}
		// The newest message is decided about from the FIRST tick onward,
		// backlog or no backlog: that is what the third number buys, and its
		// absence is what made new messages wait behind old ones.
		if at.forwardFrom() < backlog {
			t.Fatalf("tick %d ended with the newest message unread (forwardFrom=%d)", ticks, at.forwardFrom())
		}
	}
	if at.floor != backlog {
		t.Errorf("floor = %d, want the whole feed decided about (%d)", at.floor, backlog)
	}
	if at.top != 0 {
		t.Errorf("top = %d, want it collapsed into the floor once the gap closed", at.top)
	}
	// Ceil(1000/200) = 5 ticks of work; anything near 20 is the quadratic
	// re-walk this test exists to refuse.
	if ticks > 8 {
		t.Errorf("the backlog took %d ticks, want it proportional to its size (~5)", ticks)
	}
}

// New arrivals during a backfill are read on the tick they arrive, not after
// the backlog clears.
func TestMessagesArrivingDuringABackfillAreReadFirst(t *testing.T) {
	provider := &fakeProvider{items: feedOf(600), pageSize: maxPageSize}
	at := tickOnce(t, provider, cursor{floor: 5})
	if !at.unread() {
		t.Fatal("the fixture did not produce a backlog, so this test proves nothing")
	}
	// Five new messages arrive while the backlog is still open.
	provider.items = append(feedOf(605)[:5], provider.items...)

	next := tickOnce(t, provider, at)
	if next.forwardFrom() < 605 {
		t.Fatalf("the newest message (605) was not read while a backlog was open; cursor = %+v", next)
	}
}

// A feed that never grows and has been fully read stays still: the tick reads
// the newest page, finds nothing above the floor, and writes the same cursor.
func TestATickOverAQuietFeedChangesNothing(t *testing.T) {
	provider := &fakeProvider{items: feedOf(20), pageSize: maxPageSize}
	at := tickOnce(t, provider, cursor{})
	quiet := tickOnce(t, provider, at)
	if quiet != at {
		t.Errorf("cursor moved from %+v to %+v over a feed nothing was added to", at, quiet)
	}
}
