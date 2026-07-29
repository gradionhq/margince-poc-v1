ALTER TABLE capture_backfill
  DROP COLUMN inflight_scanned,
  DROP COLUMN inflight_captured,
  DROP COLUMN inflight_skipped,
  DROP COLUMN inflight_people,
  DROP COLUMN inflight_organizations;
