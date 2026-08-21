-- Dropping the columns takes an ACCESS EXCLUSIVE lock on organization, which
-- blocks every reader and writer of it. Bounded so a rollback on a busy
-- installation fails fast rather than queueing behind an open transaction and
-- holding the whole table while it waits.
SET LOCAL lock_timeout = '3s';

DROP TRIGGER IF EXISTS trg_organization_geocode_stale ON organization;
DROP FUNCTION IF EXISTS organization_geocode_goes_stale();
DROP TABLE IF EXISTS geocode_cache;
DROP TABLE IF EXISTS organization_geocode_state;
DROP INDEX IF EXISTS idx_organization_geocoded;
ALTER TABLE organization
  DROP COLUMN IF EXISTS geocode_lat,
  DROP COLUMN IF EXISTS geocode_lon,
  DROP COLUMN IF EXISTS geocoded_at,
  DROP COLUMN IF EXISTS geocode_provider,
  DROP COLUMN IF EXISTS geocode_status,
  DROP COLUMN IF EXISTS geocode_input_hash;
