ALTER TABLE capture_backfill
  ADD COLUMN inflight_people        int NOT NULL DEFAULT 0,
  ADD COLUMN inflight_organizations int NOT NULL DEFAULT 0;
