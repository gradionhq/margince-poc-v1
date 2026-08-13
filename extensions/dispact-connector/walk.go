// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package dispact

// The backwards walk and the cursor arithmetic, kept apart from the poll that
// drives them because this is the part that is easy to get wrong and worth
// reading on its own.
//
// THE CURSOR IS TWO NUMBERS, and the reason is that one cannot express what
// happens when a walk runs out of budget. mark is the forward watermark: every
// id at or below it has been decided about. gap is where a truncated backwards
// walk stopped, and while it is set the mark does NOT move — because moving it
// would put the mark above a region nothing has read, and the next tick's
// newest page would already be below it. Everything under the gap would then be
// invisible forever, with nothing anywhere reporting a loss.

import (
	"context"
	"fmt"
)

// maxPagesPerPoll bounds one tick's paging. Four pages of 50 is 200
// notifications per connection per tick; at the dispatcher's cadence a member
// receiving more than that sustainedly is one whose feed the poll is walking
// permanently behind, which the gap makes visible rather than hiding.
const maxPagesPerPoll = 4

// walkResult is what one backwards walk found.
type walkResult struct {
	// items are every notification fetched, in the provider's own order
	// (newest first). Filtering happens after, so that the cursor can advance
	// past what was filtered.
	items []inboxItem
	// closed reports whether the walk reached the previous mark or the start
	// of the feed. When it did, the region between the mark and the newest id
	// has been seen in full and the mark may move to the top of it.
	closed bool
	// oldest is the lowest id fetched, which is where a truncated walk resumes
	// next tick.
	oldest int64
}

// walkInbox pages backwards from `from` until it reaches the mark, runs out of
// feed, or spends the page budget.
//
// from is zero for a walk that starts at the newest page, and otherwise the gap
// a previous truncated walk left. budget is separate from maxPagesPerPoll
// because the FIRST poll of a connection deliberately takes one page (see
// pollConnection): connecting an account does not import its history.
func walkInbox(ctx context.Context, api *client, mark, from int64, budget int) (walkResult, error) {
	result := walkResult{}
	before := from
	for page := 0; page < budget; page++ {
		fetched, err := api.inbox(ctx, before)
		if err != nil {
			return walkResult{}, err
		}
		for _, item := range fetched.Items {
			if item.ID <= mark {
				// The mark is reached: everything below has been decided
				// about already, and the walk is complete by definition.
				result.closed = true
				return result, nil
			}
			result.items = append(result.items, item)
			result.oldest = item.ID
			before = item.ID
		}
		if !fetched.HasMore {
			// The start of the feed. There is nothing under this walk, so it
			// is closed for the same reason reaching the mark closes it.
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

// advanced is the cursor a tick ends with, and the whole of the rule.
//
// processedTo is the highest id this tick DECIDED ABOUT — landed, skipped by
// the core, filtered here, or refused as unrepresentable. Everything below it
// within the walk was decided too, because the poll works oldest-first and
// stops at the first systemic failure.
//
// The three cases, and each one is a defect an earlier version had:
//
//   - A walk that did not close advances the mark NOT AT ALL, and remembers
//     where it stopped. Advancing to the top of a truncated walk strands
//     everything under the gap permanently.
//   - A closed walk advances past everything it decided about — including what
//     was filtered. A mark that moved only past what LANDED would re-page a
//     feed of reactions on every tick, forever.
//
// The third case never reaches this function: a tick that stopped on a systemic
// failure writes no cursor at all, which is its caller's decision to make. The
// cost is a deduplicated replay of what did land; the alternative costs the
// record the failure stopped at.
func advanced(mark, gap, processedTo int64, result walkResult) (newMark, newGap int64) {
	switch {
	case !result.closed:
		// The mark stays where it was and the gap moves down to where this
		// walk ran out of budget, so the next tick resumes under it.
		return mark, result.oldest
	case processedTo > mark:
		// The gap, if there was one, is closed by definition: a closed walk
		// reached the mark from above.
		return processedTo, 0
	default:
		// A closed walk that decided about nothing new — an empty feed, or one
		// whose every item was already under the mark. The gap still clears:
		// the region it named has now been read.
		return mark, 0
	}
}
