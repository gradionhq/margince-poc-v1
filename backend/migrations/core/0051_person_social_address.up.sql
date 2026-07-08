-- 0051: structured shapes stop hiding in jsonb (issue #16). An address
-- has a fixed shape (the contract's Address schema), so it becomes
-- columns on person and organization; a person's social presence is a
-- (platform, handle) set, so it becomes a queryable relation. The
-- genuinely schemaless jsonb columns (audit images, envelopes, raw
-- capture payloads, snapshots) are untouched.

CREATE TABLE person_social (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  person_id    uuid NOT NULL,
  platform     text NOT NULL,               -- 'linkedin', 'x', … (workspace vocabulary, not a closed set)
  handle       text NOT NULL,               -- profile URL or handle as captured
  created_at   timestamptz NOT NULL DEFAULT now(),
  -- Composite same-workspace FK (C4, the 0019 convention): a
  -- cross-workspace person reference is rejected by the database.
  CONSTRAINT person_social_person_id_fkey FOREIGN KEY (workspace_id, person_id)
    REFERENCES person (workspace_id, id) ON DELETE CASCADE,
  UNIQUE (workspace_id, person_id, platform)
);
CREATE INDEX idx_person_social_person ON person_social (person_id);

ALTER TABLE person_social ENABLE ROW LEVEL SECURITY;
ALTER TABLE person_social FORCE ROW LEVEL SECURITY;
CREATE POLICY person_social_tenant_isolation ON person_social
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

-- Backfill from the jsonb map: every key/value pair becomes one row.
INSERT INTO person_social (workspace_id, person_id, platform, handle)
SELECT p.workspace_id, p.id, kv.key, kv.value
FROM person p, jsonb_each_text(p.social) AS kv
WHERE kv.value IS NOT NULL AND kv.value <> '';

-- The contract's Address shape as columns, on both carriers.
ALTER TABLE person
  ADD COLUMN address_line1       text NULL,
  ADD COLUMN address_line2       text NULL,
  ADD COLUMN address_city        text NULL,
  ADD COLUMN address_region      text NULL,
  ADD COLUMN address_postal_code text NULL,
  ADD COLUMN address_country     text NULL;  -- ISO-3166 alpha-2

UPDATE person SET
  address_line1       = address->>'line1',
  address_line2       = address->>'line2',
  address_city        = address->>'city',
  address_region      = address->>'region',
  address_postal_code = address->>'postal_code',
  address_country     = address->>'country'
WHERE address IS NOT NULL;

ALTER TABLE organization
  ADD COLUMN address_line1       text NULL,
  ADD COLUMN address_line2       text NULL,
  ADD COLUMN address_city        text NULL,
  ADD COLUMN address_region      text NULL,
  ADD COLUMN address_postal_code text NULL,
  ADD COLUMN address_country     text NULL;  -- ISO-3166 alpha-2

UPDATE organization SET
  address_line1       = address->>'line1',
  address_line2       = address->>'line2',
  address_city        = address->>'city',
  address_region      = address->>'region',
  address_postal_code = address->>'postal_code',
  address_country     = address->>'country'
WHERE address IS NOT NULL;

ALTER TABLE person DROP COLUMN social, DROP COLUMN address;
ALTER TABLE organization DROP COLUMN address;
