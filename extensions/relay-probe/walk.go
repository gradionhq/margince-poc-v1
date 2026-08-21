// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package relayprobe

// The backwards walk and the cursor arithmetic, kept apart from the poll that
// drives them because this is the part that is easy to get wrong and worth
// reading on its own.
//
// THE CURSOR IS THREE NUMBERS, and each one exists because two could not say
// what a truncated walk leaves behind:
//
//   - floor — every id at or below it has been decided about, and nothing ever
//     looks under it again.
//   - gap — where an unread region resumes, or zero for none. While it is set,
//     the FLOOR does not move: moving it would put the floor above ids nothing
//     has read, and no later walk would ever go under it.
//   - top — the highest id decided about ABOVE an unread region, or zero when
//     there is no such region. It is what lets each tick read the newest
//     messages first even while a backlog is still being walked, and it becomes
//     the new floor the moment the gap closes.
//
// The shape is core capture's own: a forward watermark that keeps up with
// what is arriving, and a backward token that fills in history, disjoint by
// construction.

import (
	"context"
	"fmt"
)

// maxPagesPerPoll bounds one tick's paging across BOTH walks. Four pages of 50
// is 200 notifications per connection per tick; a member receiving more than
// that sustainedly is one whose feed the poll is permanently behind, which the
// gap makes visible rather than hiding.
const maxPagesPerPoll = 4

// firstPollPages is what a connection that has never polled reads: one page,
// and no backfill. Connecting an account brings the CRM what arrives from now
// on — importing a member's message history is a decision with a scope and a
// cost, and not one a token paste should make silently.
const firstPollPages = 1

// cursor is where a connection has read to.
type cursor struct {
	floor int64
	gap   int64
	top   int64
}

// unread reports whether there is a region below the newest messages that
// nothing has read.
func (c cursor) unread() bool { return c.gap > 0 }

// firstPoll reports whether this connection has never read anything.
func (c cursor) firstPoll() bool { return c.floor == 0 && c.gap == 0 && c.top == 0 }

// forwardFrom is the id a forward walk stops at: the top of what is already
// decided about, which is `top` while a backlog is open and the floor
// otherwise.
func (c cursor) forwardFrom() int64 {
	if c.top > 0 {
		return c.top
	}
	return c.floor
}

// walkResult is what one backwards walk found.
type walkResult struct {
	// items are every notification fetched, in the provider's own order
	// (newest first). Filtering happens after, so that the cursor can advance
	// past what was filtered.
	items []inboxItem
	// closed reports whether the walk reached its stop id or the start of the
	// feed. When it did, the region it covered has been seen in full.
	closed bool
	// oldest is the lowest id fetched, which is where a truncated walk resumes
	// next tick.
	oldest int64
}

// walkInbox pages backwards from `from` until it reaches `until`, runs out of
// feed, or spends the page budget.
//
// from is zero for a walk that starts at the newest page, and otherwise the id
// to resume under. until is the id the walk stops AT — everything at or below
// it has been decided about already.
func walkInbox(ctx context.Context, api *client, until, from int64, budget int) (walkResult, error) {
	result := walkResult{}
	before := from
	for range budget {
		fetched, err := api.inbox(ctx, before)
		if err != nil {
			return walkResult{}, err
		}
		for _, item := range fetched.Items {
			if item.ID <= until {
				// The stop id is reached: everything below has been decided
				// about, so this walk is complete by definition.
				result.closed = true
				return result, nil
			}
			result.items = append(result.items, item)
			result.oldest = item.ID
			before = item.ID
		}
		if !fetched.HasMore {
			// The start of the feed. There is nothing under this walk, so it
			// is closed for the same reason reaching the stop id closes it.
			result.closed = true
			return result, nil
		}
		if len(fetched.Items) == 0 {
			// has_more with an empty page would page forever on the same
			// `before`. A provider that says both is one this unit stops
			// believing rather than looping against.
			return result, fmt.Errorf("%w: a page carried no items and still reported more", errProvider)
		}
	}
	return result, nil
}

// afterForward is the cursor a tick ends with after reading the NEWEST region.
//
// processedTo is the highest id this walk decided about — landed, skipped by
// the core, filtered here, or refused as unrepresentable — and zero when the
// walk found nothing new.
//
// The three cases, and each one is a defect an earlier version had:
//
//   - A FIRST poll keeps nothing below the page it read: the floor jumps to
//     the top of it and no gap is recorded, which is what makes "connecting
//     does not import your history" true rather than merely written down. An
//     earlier version recorded a gap here and then walked the member's whole
//     past, one page a tick, while never reading anything new.
//   - A walk that did not close leaves an unread region behind it, so the gap
//     moves to where it stopped and the FLOOR stays. What it does NOT do is
//     stay still: the ids it did decide about are recorded as `top`, so the
//     next tick reads from there rather than re-walking the burst — and new
//     messages keep arriving on the timeline while the backlog is filled in.
//   - A closed walk with no gap under it advances the floor and clears `top`,
//     which is the ordinary tick.
func afterForward(before cursor, processedTo int64, result walkResult) cursor {
	after := before
	if processedTo > 0 {
		after.top = processedTo
	}
	switch {
	case before.firstPoll():
		after.floor, after.top, after.gap = processedTo, 0, 0
	case !result.closed:
		after.gap = result.oldest
	case !after.unread():
		after.floor, after.top = maxOf(after.top, before.floor), 0
	}
	return after
}

// afterBackfill is the cursor after the walk that fills in an unread region.
//
// Closing the gap is what finally moves the floor: the region between the floor
// and the top has now been read in full, so the two collapse into one number
// again. A backfill that ran out of budget just moves the gap down, and the
// next tick resumes under it — with the newest messages still read first,
// because the forward walk runs before this one.
func afterBackfill(before cursor, result walkResult) cursor {
	after := before
	if result.closed {
		after.floor, after.gap, after.top = maxOf(before.top, before.floor), 0, 0
		return after
	}
	if result.oldest > 0 {
		after.gap = result.oldest
	}
	return after
}

func maxOf(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
