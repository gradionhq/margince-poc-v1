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
  ADD COLUMN geocode_input_hash text NULL,
  -- A resolved row HAS a point, and a point is on the earth. Without this the
  -- status vocabulary is the only rule, so 'ok' with null coordinates — or a
  -- transposed lat/lon pair — is a legal row that a radius query would read.
  -- The wrong-distance failure is the invisible one: it answers, and the answer
  -- looks like every other answer.
  ADD CONSTRAINT organization_geocode_resolved_has_a_point CHECK (
    geocode_status IS DISTINCT FROM 'ok'
    OR (geocode_lat IS NOT NULL AND geocode_lon IS NOT NULL
        AND geocode_lat BETWEEN -90 AND 90
        AND geocode_lon BETWEEN -180 AND 180)
  );

-- Partial: only resolved, live rows are ever selected by a radius query, and
-- they are a minority of the table on any workspace that has not finished
-- ingesting. Indexing the rest would cost writes to serve no read.
--
-- Built in the same transaction as the ALTER above, which means the ACCESS
-- EXCLUSIVE lock that ALTER took is held while it builds. That is acceptable
-- here and would not be on a large table: the predicate matches only rows with
-- geocode_status = 'ok', and no row has that status until the worker has run,
-- so at the moment this migration applies the index covers nothing and builds
-- instantly. An installation adding these columns to a table that ALREADY held
-- coordinates would want CREATE INDEX CONCURRENTLY outside a transaction.
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

-- THE INVALIDATION IS A TRIGGER, not a rule each writer remembers.
--
-- The first cut put it in Go, in the two address writers that were easy to
-- find. A review found four more — the cold-start fill, the site-read
-- overwrite, the company form, and organization creation — each of which
-- writes an address through its own table-driven SQL, several columns deep in
-- a generic builder with no room to carry a seam through it. Six writers is
-- already too many to hold in anyone's head, and the seventh would be added by
-- somebody who never read this file.
--
-- The defect it prevents is invisible: a company whose address moved keeps
-- answering radius queries from where it used to be, reporting success. So the
-- rule belongs where it cannot be bypassed. A writer that changes any address
-- column and does NOT set geocode_status itself gets 'stale' — and one that
-- sets it explicitly (the worker recording a fresh point) is left alone.
CREATE FUNCTION organization_geocode_goes_stale() RETURNS trigger AS $$
BEGIN
  -- Only when the coordinates exist and the writer did not speak for them:
  -- stamping 'stale' on a row that was never resolved would say the
  -- coordinates are out of date rather than absent, and overriding a writer
  -- that set the status deliberately would undo the worker's own write.
  IF NEW.geocode_status IS DISTINCT FROM OLD.geocode_status THEN
    RETURN NEW;
  END IF;
  IF OLD.geocode_status IS NULL THEN
    RETURN NEW;
  END IF;
  NEW.geocode_status := 'stale';
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_organization_geocode_stale
  BEFORE UPDATE OF address_line1, address_line2, address_city,
                   address_region, address_postal_code, address_country
  ON organization
  FOR EACH ROW
  WHEN (
    OLD.address_line1       IS DISTINCT FROM NEW.address_line1 OR
    OLD.address_line2       IS DISTINCT FROM NEW.address_line2 OR
    OLD.address_city        IS DISTINCT FROM NEW.address_city OR
    OLD.address_region      IS DISTINCT FROM NEW.address_region OR
    OLD.address_postal_code IS DISTINCT FROM NEW.address_postal_code OR
    OLD.address_country     IS DISTINCT FROM NEW.address_country
  )
  EXECUTE FUNCTION organization_geocode_goes_stale();

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
