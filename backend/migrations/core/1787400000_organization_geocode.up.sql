-- Coordinates for a company, so "which customers are near Stuttgart" can be
-- answered by the query surface instead of refused.
--
-- WHAT IS DELIBERATELY ABSENT: PostGIS, cube, earthdistance. Two double
-- precision columns and a haversine in SQL answer this question at this scale,
-- and every one of those extensions would have to be added to the migration
-- role's allowlist — a permanent widening of what migrations may install, for
-- a distance calculation that is fourteen lines of arithmetic.
--
-- geocode_status is the column the query predicate reads, NOT the coordinates.
-- An address that changed has stale coordinates until the worker catches up,
-- and a query that read lat/lon alone would answer distances from the previous
-- address while reporting success. The writer sets 'stale' in the same
-- transaction as the address change, and only 'ok' is queryable.
--
-- geocode_input_hash is what makes reingestion cheap: the worker skips an
-- address it has already resolved, so re-reading a company's website does not
-- spend a lookup on an address that has not moved.

SET LOCAL lock_timeout = '3s';

ALTER TABLE organization
  ADD COLUMN geocode_lat        double precision NULL,
  ADD COLUMN geocode_lon        double precision NULL,
  ADD COLUMN geocoded_at        timestamptz NULL,
  ADD COLUMN geocode_provider   text NULL,
  ADD COLUMN geocode_status     text NULL
    CHECK (geocode_status IS NULL OR geocode_status IN ('ok', 'failed', 'no_match', 'stale')),
  ADD COLUMN geocode_input_hash text NULL;

-- Partial: only resolved, live rows are ever selected by a radius query, and
-- they are a minority of the table on any workspace that has not finished
-- ingesting. Indexing the rest would cost writes to serve no read.
CREATE INDEX idx_organization_geocoded
  ON organization (geocode_lat, geocode_lon)
  WHERE geocode_status = 'ok' AND archived_at IS NULL;

-- The attempt ledger, modelled on capture_auto_enrich_state: a company whose
-- address the geocoder cannot resolve must not be retried forever, and the
-- next_attempt_at is what spaces out the ones worth retrying.
--
-- No workspace column. Migration 0217 retired row-level security and the
-- tables authored since carry no workspace key (0262, 0282); organization_id
-- is the scope, and the reads that use this table go through the same gate
-- every other organization read does.
CREATE TABLE organization_geocode_state (
  organization_id uuid PRIMARY KEY REFERENCES organization(id) ON DELETE CASCADE,
  attempts        int NOT NULL DEFAULT 0,
  last_outcome    text NULL,
  next_attempt_at timestamptz NULL,
  updated_at      timestamptz NOT NULL DEFAULT now()
);

-- The place cache: a name resolved to a point, shared across the workspace.
--
-- MANDATORY, not an optimisation. Nominatim's usage policy requires that a
-- client which runs regularly caches its results, and the alternative is
-- re-asking a free public service for the coordinates of Stuttgart every time
-- somebody types it.
--
-- The key is the normalized query text, so "Stuttgart" and " stuttgart " are
-- one entry rather than two.
CREATE TABLE geocode_cache (
  query      text PRIMARY KEY,
  lat        double precision NOT NULL,
  lon        double precision NOT NULL,
  provider   text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
