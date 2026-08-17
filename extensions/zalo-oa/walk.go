// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package zalooa

// The backwards walk and the cursor arithmetic, kept apart from the poll that
// drives them because this is the part that is easy to get wrong and worth
// reading on its own.
//
// THE CURSOR IS OVER TIME, NOT OVER A POSITION, and that is forced by the
// provider: `listrecentchat` is a GLOBAL newest-first walk paged by OFFSET, and
// an offset shifts every time a message arrives. A position is therefore not a
// resumable identity; a message's own timestamp is. What an offset is still good
// for is a HINT about where to resume, which is the fourth number below.
//
// THE FIRST THREE NUMBERS, each of which exists because two could not say what a
// truncated walk leaves behind:
//
//   - floor — every message at or before it has been decided about, and nothing
//     ever looks under it again.
//   - gap — where an unread region resumes, or zero for none. While it is set,
//     the FLOOR does not move: moving it would put the floor above messages
//     nothing has read, and no later walk would ever go under it.
//   - top — the newest message decided about ABOVE an unread region, or zero
//     when there is no such region. It is what lets each tick read the newest
//     messages first even while a backlog is still being walked, and it becomes
//     the new floor the moment the gap closes.
//
// The shape is core capture's own: a forward watermark that keeps up with what
// is arriving, and a backward token that fills in history, disjoint by
// construction.

import "context"

// maxPagesPerPoll bounds one tick's paging across BOTH walks. Six pages of ten
// is 60 messages per tick; at the two-minute cadence that is 30 a minute
// sustained, and an account busier than that is one the poll is permanently
// behind — which the gap makes visible rather than hiding.
const maxPagesPerPoll = 6

// firstPollPages is what a connection that has never polled reads: one page, and
// no backfill. Connecting an account brings the CRM what arrives from now on —
// importing an OA's message history is a decision with a scope and a cost, and
// not one an authorization click should make silently.
const firstPollPages = 1

// cursor is where a connection has read to.
type cursor struct {
	floor int64
	gap   int64
	top   int64
	// offset is where in the global walk the unread region began when the walk
	// ran out of budget. It is a HINT: the provider shifts every offset as
	// messages arrive, so poll.go adjusts it by what has landed since and
	// deliberately resumes one page shallower than the arithmetic says. Reading
	// a page again costs a deduplicated no-op; missing one costs the messages in
	// it.
	offset int
}

// unread reports whether there is a region below the newest messages that
// nothing has read.
func (c cursor) unread() bool { return c.gap > 0 }

// firstPoll reports whether this connection has never read anything.
func (c cursor) firstPoll() bool { return c.floor == 0 && c.gap == 0 && c.top == 0 }

// forwardFrom is the timestamp a forward walk stops at: the top of what is
// already decided about, which is `top` while a backlog is open and the floor
// otherwise.
func (c cursor) forwardFrom() int64 {
	if c.top > 0 {
		return c.top
	}
	return c.floor
}

// walkSpec is one backwards walk: where it starts, where it stops, and what it
// declines to collect on the way.
type walkSpec struct {
	// stopBelow ends the walk at the first message OLDER than this timestamp.
	//
	// The boundary is INCLUSIVE on purpose — a message whose time equals it is
	// collected and decided again. Zalo timestamps are milliseconds and two
	// messages can share one, so an exclusive boundary would drop the second of
	// any such pair forever. Re-deciding the boundary costs one deduplicated
	// replay per tick, because the natural key makes a re-land a no-op.
	stopBelow int64
	// skipAbove declines to collect anything NEWER than this timestamp while
	// still paging past it. It is how a backfill resumes: the pages above the
	// unread region are walked through rather than around, because the provider
	// offers no way to seek to a time.
	//
	// It is strictly-above for the same reason stopBelow is inclusive, and the
	// two boundaries are the same rule seen from each end. A truncated walk stops
	// at a PAGE edge, which can fall in the middle of several messages sharing
	// one millisecond — so a skip that also dropped the ties would lose every
	// sibling of the boundary message, permanently, while re-collecting them
	// costs a deduplicated replay.
	//
	// Zero collects everything.
	skipAbove int64
	// startPage is where to begin, in pages of maxChatPage.
	startPage int
	// budget is how many pages this walk may spend.
	budget int
}

// walkResult is what one backwards walk found.
type walkResult struct {
	// items are every message collected, in the provider's own order (newest
	// first). Filtering happens after, so the cursor can advance past what was
	// filtered.
	items []chatMessage
	// closed reports whether the walk reached its stop point or the start of the
	// feed. When it did, the region it covered has been seen in full.
	closed bool
	// oldest is the lowest timestamp collected, which is where a truncated walk
	// resumes next tick.
	oldest int64
	// stoppedAtPage is the page a truncated walk did not get past, kept as the
	// resume hint.
	stoppedAtPage int
}

// walkChats pages backwards until it passes the stop point, runs out of feed, or
// spends the page budget.
//
// Paging terminates on a SHORT PAGE and never on an error: an offset past the
// end of the feed answers success with no rows, which is measured behaviour and
// not an assumption — so "fewer rows than asked for" is the end of the walk.
func walkChats(ctx context.Context, api *client, spec walkSpec) (walkResult, error) {
	result := walkResult{stoppedAtPage: spec.startPage}
	for page := spec.startPage; page < spec.startPage+spec.budget; page++ {
		result.stoppedAtPage = page
		fetched, err := api.recentChat(ctx, chatPageOffset(page))
		if err != nil {
			return walkResult{}, err
		}
		for _, row := range fetched {
			if row.Time < spec.stopBelow {
				// Past the stop point: everything from here down has been
				// decided about, so this walk is complete by definition.
				result.closed = true
				return result, nil
			}
			if spec.skipAbove > 0 && row.Time > spec.skipAbove {
				// Above the region this walk is filling. It is paged through
				// rather than sought past, because the provider has no seek.
				continue
			}
			result.items = append(result.items, row)
			result.oldest = row.Time
		}
		if len(fetched) < maxChatPage {
			// The start of the feed. There is nothing under this walk, so it is
			// closed for the same reason reaching the stop point closes it.
			result.closed = true
			return result, nil
		}
	}
	return result, nil
}

// afterForward is the cursor a tick ends with after reading the NEWEST region.
//
// decidedTo is the newest timestamp this walk decided about — landed, skipped by
// the core, or refused as unrepresentable — and zero when the walk found nothing
// new.
//
// The three cases:
//
//   - A FIRST poll keeps nothing below the page it read: the floor jumps to the
//     top of it and no gap is recorded, which is what makes "connecting does not
//     import your history" true rather than merely written down.
//   - A walk that did not close leaves an unread region behind it, so the gap
//     moves to where it stopped and the FLOOR stays. What it does NOT do is stay
//     still: the messages it did decide about are recorded as `top`, so the next
//     tick reads from there rather than re-walking the burst. Note that this
//     moves the gap UP over a region an earlier backfill had already decided —
//     deliberately, because the alternative is tracking two disjoint holes. The
//     cost is re-reading messages that then re-land as no-ops; the safe direction
//     is the one that reads too much.
//   - A closed walk with no gap under it advances the floor and clears `top`,
//     which is the ordinary tick.
func afterForward(before cursor, decidedTo int64, result walkResult) cursor {
	after := before
	if decidedTo > 0 {
		after.top = decidedTo
	}
	switch {
	case before.firstPoll():
		after.floor, after.top, after.gap, after.offset = decidedTo, 0, 0, 0
	case !result.closed:
		after.gap, after.offset = result.oldest, result.stoppedAtPage*maxChatPage
	case !after.unread():
		after.floor, after.top, after.offset = maxOf(after.top, before.floor), 0, 0
	}
	return after
}

// afterBackfill is the cursor after the walk that fills in an unread region.
//
// Closing the gap is what finally moves the floor: the region between the floor
// and the top has now been read in full, so the two collapse into one number
// again. A backfill that ran out of budget just moves the gap down, and the next
// tick resumes under it — with the newest messages still read first, because the
// forward walk runs before this one.
func afterBackfill(before cursor, result walkResult) cursor {
	after := before
	if result.closed {
		after.floor, after.gap, after.top, after.offset = maxOf(before.top, before.floor), 0, 0, 0
		return after
	}
	if result.oldest > 0 {
		after.gap, after.offset = result.oldest, result.stoppedAtPage*maxChatPage
	}
	return after
}

// resumePage is where a backfill picks up, given how many messages the forward
// walk has decided about since the gap was recorded.
//
// The offset stored with the gap counted the messages above it AT THAT MOMENT;
// every message that has arrived since pushes the whole feed down by one. So the
// arithmetic is "where it was, plus what has arrived" — and then ONE PAGE
// SHALLOWER than that, because the arithmetic assumes nothing was deleted and a
// resume that lands too deep skips messages permanently while one that lands too
// shallow re-reads a page it will discard.
func resumePage(at cursor, arrivedSince int) int {
	page := (at.offset+arrivedSince)/maxChatPage - 1
	if page < 0 {
		return 0
	}
	return page
}

// pagesSpent reports how many pages a walk of n collected messages cost, which
// is what the second walk of a tick has left to spend.
func pagesSpent(collected int) int {
	return collected/maxChatPage + 1
}

// maxOf is the larger of two timestamps.
func maxOf(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
