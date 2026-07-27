// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

import "time"

// watermarkFloorSkewGrace backdates the connection-derived floor to absorb
// clock skew between OUR database and the incumbent's clock. connectedAt is
// the only value in the sweep window that does not come from the incumbent:
// Postgres stamps it (incumbent_connection.connected_at defaults to now(), and
// a reconnect sets now()), while a persisted watermark is always an
// incumbent-generated ModifiedAt. The incumbent then evaluates the resulting
// filter against its OWN timestamps. If our database clock runs ahead by δ, a
// record the incumbent stamps within δ of the connect falls below the floor and
// is never swept. Re-reading a few extra minutes is free (the mirror ingest is
// an idempotent upsert guarded on a strictly-newer baseline); missing a record
// is silent, so the trade is one-sided.
const watermarkFloorSkewGrace = 15 * time.Minute

// ReconcileFloor answers the instant an incremental sweep of one object class
// should read from, given that class's persisted watermark (the zero time when
// it has none) and the connection's connectedAt.
//
// A class with no watermark yet would otherwise sweep from the zero time, which
// the HubSpot adapter renders as `lastmodifieddate GTE 0` — the entire portal,
// page by page, immediately after Backfill already read it. That wastes a full
// portal's Search quota on every connect, and it defeats the
// MARGINCE_OVERLAY_BACKFILL_LIMIT dev cap outright, since the capped walk takes
// N records and the very next sweep pulls down all the rest anyway.
//
// The floor is DERIVED from the connection rather than persisted alongside the
// watermark, so no write has to happen for it to exist — and a checkpoint that
// was never recorded is exactly the failure this prevents. The obligation moves
// rather than vanishing, though: a Reconcile call site that passes a raw
// watermark instead of this floor reintroduces the bug verbatim.
//
// connectedAt is a sound floor because the backfill walk cannot have started
// before it: everything below the floor is the BACKFILL's responsibility, not
// the incremental sweep's. That is the honest form of the claim — under
// MARGINCE_OVERLAY_BACKFILL_LIMIT the walk deliberately declines records below
// the floor, and mid-walk it has not reached them yet, so "the walk covered
// them" would be false in both cases.
//
// It never lowers a watermark. It does raise one that sits below the floor,
// which is safe because such a watermark predates this connection: Disconnect
// purges overlay_reconcile_watermark in the same transaction that revokes, and
// a reconnect can only revive a revoked row — so a sub-floor watermark cannot
// be a live checkpoint whose progress would be skipped.
func ReconcileFloor(watermark, connectedAt time.Time) time.Time {
	floor := connectedAt.Add(-watermarkFloorSkewGrace)
	if watermark.After(floor) {
		return watermark
	}
	return floor
}
