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
