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
// Owners/Name pass straight through, so continuous sync stays uncapped by
// design: a record edited AFTER the sweep's watermark still trickles in on a
// later tick even when the cap declined it here, and the ingest guards make
// the extras harmless.
//
// Capping Backfill alone bounds nothing the next sweep would not undo. It
// holds only while the sweep raises the window of a class that has no
// watermark yet (overlay.ReconcileFloor, which documents why): read from the
// zero time and the Modified pass pulls down every record the cap just
// declined, so the laptop gets the whole portal one tick later. Capping
// Modified is NOT the alternative — truncating a watermark-ordered page
// stalls the watermark whenever more than limit records share a timestamp
// (a bulk import), and the sweep then re-reads that same page forever.
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
//
// Now that the floor actually makes the cap hold (the paragraph above), a
// capped class's records below the cap are permanently declined — so it
// marks every page it cuts short as overlay.Page.Truncated, sticky into
// overlay_backfill_cursor.truncated (backfill.go/mirrorcheckpoints.go),
// which backfillCompleteFor (syncstatus.go) reads back so SyncStatus never
// reports a capped class complete. done still retires it (re-listing under
// the same cap would relearn nothing), but done and "genuinely complete"
// are no longer the same claim.
//
// Recovery is NOT automatic once a class converges truncated: unsetting
// MARGINCE_OVERLAY_BACKFILL_LIMIT does not resume it — done=true still
// short-circuits Backfill (backfill.go) before the raw adapter is ever
// reached again, so truncated never clears on its own. The operator action
// is the same one the mid-flight caveat above names: reset that object
// class's overlay_backfill_cursor row (or reconnect, which purges it) so
// the next sweep backfills it for real, this time uncapped.
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
		// further listing (an empty terminal page, NextCursor ""). Truncated
		// stays true: the incumbent's own list was never exhausted, only
		// declined.
		return overlay.Page{Truncated: true}, nil
	}
	page, err := c.Incumbent.Backfill(ctx, objectClass, inner)
	if err != nil {
		return page, err
	}
	remaining := c.limit - consumed
	if len(page.Records) > remaining {
		// The cap lands mid-page: keep only what fits and stop here,
		// regardless of whether the incumbent's own list had more.
		page.Records = page.Records[:remaining]
		page.NextCursor = ""
		page.Truncated = true
		return page, nil
	}
	consumed += len(page.Records)
	if page.NextCursor == "" {
		// The incumbent's own list is exhausted exactly here — a genuine
		// convergence, not a decline, even if it lands exactly at the cap.
		return page, nil
	}
	if consumed >= c.limit {
		// A non-empty NextCursor is the incumbent's own signal that its list
		// has more (true for HubSpot v3 paging and the fake; an incumbent
		// that handed out a cursor to an empty terminal page would read as
		// truncated forever, but the failure direction stays conservative —
		// reports incomplete, never falsely complete). The cap says stop
		// regardless — this IS a decline, not the incumbent's own end of list.
		page.NextCursor = ""
		page.Truncated = true
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
