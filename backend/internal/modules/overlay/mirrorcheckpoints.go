// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package overlay

// This file owns the sync-POSITION checkpoints — a distinct concern from
// the record cache in mirrorstore.go: where that store holds the mirrored
// records themselves, these are the two persisted positions into the
// incumbent's own stream that make continuous sync resumable — the backfill
// cursor (a one-time list-keyset walk that retires once done) and the
// reconcile watermark (a timestamp the incremental sweep keeps advancing).
// Both save paths carry the disconnect-race fence (disconnectfence.go): a
// checkpoint resurrected after teardown would make a later connection resume
// mid-stream, so the sweep's fenced store refuses to write one into a
// disconnected workspace — and, because a checkpoint (unlike a plain mirror
// row) is never revisited once it says done/advanced, both saves fence on
// the connection's IDENTITY (assertOwnConnection), not just its status: a
// write from a sweep that straddled a disconnect+reconnect must not land
// under the connection it no longer belongs to.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/gradionhq/margince/backend/internal/platform/database"
)

// upsertBackfillCursorSQL checkpoints the backfill cursor design.md §4.4
// requires ("checkpointed, resumable") — one row per (workspace,
// object_class), reusing the same NULLIF(current_setting(...)) pattern
// as ingestSQL/upsertAssocSQL so a caller with no workspace GUC set
// fails the NOT NULL constraint rather than checkpointing under a null
// tenant.
// done is sticky-true (done = old.done OR excluded.done): a converged
// backfill must never be knocked back to pending by an out-of-order save
// from a slower concurrent pass (the periodic poller racing an on-demand
// reconcile) — that would re-list the whole incumbent. Within one
// connection's life done only ever goes false→true (a reconnect purges the
// row and starts fresh), so OR-ing is monotonic; assertOwnConnection
// (SaveBackfillCursor, below) is what keeps that premise true across a
// disconnect+reconnect straddle, rather than leaving it an assumption.
// truncated is sticky the same way, for the same reason (a capped run's
// truncated=true must never be overwritten back to false by an earlier
// in-flight save landing after it).
const upsertBackfillCursorSQL = `
INSERT INTO overlay_backfill_cursor (workspace_id, object_class, cursor, done, truncated, updated_at)
VALUES (NULLIF(current_setting('app.workspace_id',true),'')::uuid, $1, $2, $3, $4, now())
ON CONFLICT (workspace_id, object_class) DO UPDATE
   SET cursor = EXCLUDED.cursor,
       done = overlay_backfill_cursor.done OR EXCLUDED.done,
       truncated = overlay_backfill_cursor.truncated OR EXCLUDED.truncated,
       updated_at = now()`

// BackfillProgress is one object class's backfill state as of the page just
// landed — grouped so a SaveBackfillCursor call site never reads as two
// anonymous positional bools whose meaning depends on argument order alone.
type BackfillProgress struct {
	// Done retires a converged backfill so a later call is a cheap no-op.
	Done bool
	// Truncated marks a converged run that the
	// MARGINCE_OVERLAY_BACKFILL_LIMIT cap cut short rather than the
	// incumbent's own list ending (overlay.Page.Truncated) —
	// backfillCompleteFor (syncstatus.go) reads it back so a capped class
	// is never reported complete.
	Truncated bool
}

// SaveBackfillCursor persists Backfill's (overlay/backfill.go) list cursor
// for objectClass after each page — the checkpoint a restart resumes from
// instead of re-listing the incumbent from the start. connectedAt is the
// sweep's own belief of the connection it is writing under; when fenced, it
// must still be the ACTIVE connection's identity (assertOwnConnection), not
// just an active row.
func (s *MirrorStore) SaveBackfillCursor(ctx context.Context, objectClass, cursor string, progress BackfillProgress, connectedAt time.Time) error {
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if s.fenced {
			if err := assertOwnConnection(ctx, tx, connectedAt); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, upsertBackfillCursorSQL, objectClass, cursor, progress.Done, progress.Truncated); err != nil {
			return fmt.Errorf("overlay: checkpointing the %s backfill cursor: %w", objectClass, err)
		}
		return nil
	})
}

// LoadBackfillCursor reads back objectClass's persisted backfill cursor.
// No row yet (a backfill that has never run) answers the start-of-paging
// cursor ("") and done=false — an honest "not started", not an error.
func (s *MirrorStore) LoadBackfillCursor(ctx context.Context, objectClass string) (string, bool, error) {
	var cursor string
	var done bool
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		scanErr := tx.QueryRow(
			ctx,
			`SELECT cursor, done FROM overlay_backfill_cursor WHERE object_class = $1`,
			objectClass,
		).Scan(&cursor, &done)
		if errors.Is(scanErr, pgx.ErrNoRows) {
			cursor, done = "", false
			return nil
		}
		return scanErr
	})
	if err != nil {
		return "", false, fmt.Errorf("overlay: loading the %s backfill cursor: %w", objectClass, err)
	}
	return cursor, done, nil
}

// upsertReconcileWatermarkSQL checkpoints Reconcile's (reconcile.go)
// incremental watermark — a distinct, always-on counterpart to
// upsertBackfillCursorSQL above: Backfill's cursor is a one-time,
// list-keyset walk that retires once done; this watermark is a
// timestamp the incremental sweep keeps advancing forever, so it lives
// in its own table (overlay_reconcile_watermark) rather than overloading
// the backfill cursor's "cursor" column with a second meaning.
// The watermark only ever advances (WHERE excluded.watermark > current): an
// older pass committing after a newer one must not move it backward, which
// would re-sweep the window between and, worse, risk re-ingesting records a
// newer pass already saw. The same monotonic-progress discipline ingestSQL's
// staleness guard applies to a mirror row (mirrorstore.go).
const upsertReconcileWatermarkSQL = `
INSERT INTO overlay_reconcile_watermark (workspace_id, object_class, watermark, updated_at)
VALUES (NULLIF(current_setting('app.workspace_id',true),'')::uuid, $1, $2, now())
ON CONFLICT (workspace_id, object_class) DO UPDATE
   SET watermark = EXCLUDED.watermark, updated_at = now()
   WHERE EXCLUDED.watermark > overlay_reconcile_watermark.watermark`

// SaveReconcileWatermark persists Reconcile's new watermark for objectClass
// after a sweep pass — the checkpoint the next scheduled pass resumes from
// instead of re-walking every record since epoch. connectedAt is the
// sweep's own belief of the connection it is writing under; when fenced, it
// must still be the ACTIVE connection's identity (assertOwnConnection), not
// just an active row — see SaveBackfillCursor's doc for why this checkpoint
// specifically needs identity, not just status.
func (s *MirrorStore) SaveReconcileWatermark(ctx context.Context, objectClass string, watermark, connectedAt time.Time) error {
	return database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		if s.fenced {
			if err := assertOwnConnection(ctx, tx, connectedAt); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, upsertReconcileWatermarkSQL, objectClass, watermark); err != nil {
			return fmt.Errorf("overlay: checkpointing the %s reconcile watermark: %w", objectClass, err)
		}
		return nil
	})
}

// LoadReconcileWatermark reads back objectClass's persisted incremental
// watermark. No row yet answers the zero time — an honest "not started",
// not an error — which Reconcile (reconcile.go) raises through its internal
// floor before ever sweeping from it, so this store never hands a raw zero
// time to the incumbent seam.
func (s *MirrorStore) LoadReconcileWatermark(ctx context.Context, objectClass string) (time.Time, error) {
	var watermark time.Time
	err := database.WithWorkspaceTx(ctx, s.pool, func(tx pgx.Tx) error {
		scanErr := tx.QueryRow(
			ctx,
			`SELECT watermark FROM overlay_reconcile_watermark WHERE object_class = $1`,
			objectClass,
		).Scan(&watermark)
		if errors.Is(scanErr, pgx.ErrNoRows) {
			watermark = time.Time{}
			return nil
		}
		return scanErr
	})
	if err != nil {
		return time.Time{}, fmt.Errorf("overlay: loading the %s reconcile watermark: %w", objectClass, err)
	}
	return watermark, nil
}
