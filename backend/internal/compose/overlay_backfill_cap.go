// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
	"context"
	"strconv"
	"strings"

	"github.com/gradionhq/margince/backend/internal/modules/overlay"
)

// cappedIncumbent bounds an Incumbent's Backfill at limit records per
// object class — a dev/demo convenience (MARGINCE_OVERLAY_BACKFILL_LIMIT)
// so connecting a real, large portal doesn't run an unbounded initial
// load on a laptop. It ends pagination early once limit records have been
// ingested for an object class, encoding the running count into the
// cursor it returns, so it is stateless and restart-safe: a resumed
// backfill reads the count back out of the cursor rather than needing any
// state of its own. Only Backfill is capped — Modified/Get/Associations/
// Owners/Name pass straight through, so continuous sync (the modified
// sweeps) stays uncapped by design: records edited outside the first N
// still trickle in on later ticks, and the ingest guards make the extras
// harmless.
//
// Caveat (dev/demo only): the running count is carried in the persisted
// overlay_backfill_cursor as a "<count>|<inner>" prefix. If the cap is
// REMOVED (MARGINCE_OVERLAY_BACKFILL_LIMIT unset) while a capped backfill
// is still mid-flight, the raw adapter is then handed that prefixed cursor
// and rejects it, so that object class's backfill fails every sweep
// (warn-logged) until its overlay_backfill_cursor row is reset. Changing
// the cap mid-backfill is therefore an operator action that also clears
// the cursor (or lets the class finish first). This is acceptable for a
// dev/demo knob; a production cap would carry the count out-of-band.
type cappedIncumbent struct {
	overlay.Incumbent
	limit int
}

// cappedCursorSep separates the decorator's own running-count prefix from
// the wrapped incumbent's opaque cursor. HubSpot's own `after` cursors are
// numeric ids, so a "|" never collides with one.
const cappedCursorSep = "|"

func (c cappedIncumbent) Backfill(ctx context.Context, objectClass, cursor string) (overlay.Page, error) {
	consumed, inner := splitCappedCursor(cursor)
	if consumed >= c.limit {
		// The cap was already reached on a prior page — converge with no
		// further listing (an empty terminal page, NextCursor "").
		return overlay.Page{}, nil
	}
	page, err := c.Incumbent.Backfill(ctx, objectClass, inner)
	if err != nil {
		return page, err
	}
	if remaining := c.limit - consumed; len(page.Records) > remaining {
		page.Records = page.Records[:remaining]
		page.NextCursor = "" // the truncated page carries the last records this object gets
	}
	consumed += len(page.Records)
	if consumed >= c.limit || page.NextCursor == "" {
		page.NextCursor = ""
		return page, nil
	}
	page.NextCursor = strconv.Itoa(consumed) + cappedCursorSep + page.NextCursor
	return page, nil
}

// splitCappedCursor decodes a cursor this decorator previously emitted
// ("<consumed>|<innerCursor>"). The start-of-paging "" — and any cursor
// without the count prefix — decodes to (0, cursor): nothing consumed
// yet, the whole string passed through as the wrapped incumbent's own
// cursor.
func splitCappedCursor(cursor string) (consumed int, inner string) {
	before, after, found := strings.Cut(cursor, cappedCursorSep)
	if !found {
		return 0, cursor
	}
	n, err := strconv.Atoi(before)
	if err != nil {
		return 0, cursor
	}
	return n, after
}

// overlayIncumbentFactory returns the per-connection incumbent adapter
// builder the overlay connection lifecycle and reconcile sweep resolve the
// owners directory and backfill through. When limit > 0 it wraps the live
// HubSpot adapter in cappedIncumbent so backfill is bounded; limit == 0
// (the unset default) returns the plain hubspotIncumbentFactory uncapped.
func overlayIncumbentFactory(limit int) func(region, token string) overlay.Incumbent {
	if limit <= 0 {
		return hubspotIncumbentFactory
	}
	return func(region, token string) overlay.Incumbent {
		return cappedIncumbent{Incumbent: hubspotIncumbentFactory(region, token), limit: limit}
	}
}
