-- 20260727150000_overlay_backfill_cursor_truncated: the honesty column
-- backfillCompleteFor (syncstatus.go) reads alongside done. done=true
-- correctly retires a backfill the MARGINCE_OVERLAY_BACKFILL_LIMIT dev cap
-- cut short (re-listing under the same cap would relearn nothing), but
-- done alone would let SyncStatus report that class backfill-complete when
-- the incumbent's own list was never exhausted, only declined. truncated is
-- sticky the same way done is (mirrorcheckpoints.go's upsert), for the same
-- reason: a capped run's truncated=true must never be overwritten back to
-- false by an earlier in-flight save landing after it.
ALTER TABLE overlay_backfill_cursor ADD COLUMN IF NOT EXISTS truncated boolean NOT NULL DEFAULT false;
