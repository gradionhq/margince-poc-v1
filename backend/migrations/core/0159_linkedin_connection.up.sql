-- 0159: LinkedIn connections as GHOSTS — graph substrate, never records
-- (CG-DDL-2 / ADR-0078 §2.1b).
--
-- A member's LinkedIn network is the single richest source of "who on our team
-- knows whom" that exists, and it is reachable two ways: the Member Data
-- Portability API (EU/DMA, needs an approved developer app) and the export
-- every member can download themselves, Connections.csv. This table is the
-- landing place for both — the CSV path works today, the API path is a
-- provider behind the same rows later.
--
-- THEY ARE NOT PEOPLE. A ghost never appears in search, lists, the people
-- screens, or the assistant's record tools, and nothing can write to one: no
-- timeline, no activities, no outreach. That is deliberate and it is the whole
-- safety property. A LinkedIn export is a list of third parties who never
-- consented to being in anyone's CRM; turning 3,000 of them into contacts
-- would be both a consent problem and a data-quality catastrophe. They exist
-- only to answer "does anyone here already know someone at this company".
--
-- The PK is synthetic because name+company is NOT an identity. Two people
-- called Andreas Müller can work at the same firm, and a natural key over
-- their names would silently merge them.

CREATE TABLE linkedin_connection (
  id                  uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id        uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  -- Whose network this connection belongs to. A LinkedIn connection is
  -- personal: it says THIS colleague knows them, never that the company does.
  owner_user_id       uuid NOT NULL,

  full_name           text NOT NULL,
  -- Case- and accent-folded for matching. Stored rather than computed at read
  -- so the match index is usable, and kept beside the original because the
  -- original is what a human is shown when confirming a match.
  normalized_name     text NOT NULL,
  position            text NULL,
  company_name        text NULL,
  normalized_company  text NULL,
  connected_on        date NULL,
  -- The provider's own stable id, when the API path supplies one. The CSV
  -- export carries none, which is exactly why the fallback key below exists.
  provider_member_ref text NULL,
  -- Present only on CSV rows, and only when the connection allowed their
  -- address to be exported. It is the one field that permits an exact match.
  email               text NULL,

  matched_person_id   uuid NULL,
  matched_org_id      uuid NULL,
  match_status        text NOT NULL DEFAULT 'unmatched'
                      CHECK (match_status IN ('unmatched','suggested','confirmed','rejected')),
  source              text NOT NULL CHECK (source IN ('portability_api','csv_export')),

  synced_at           timestamptz NOT NULL DEFAULT now(),
  -- A connection that disappeared from a later export. Kept rather than
  -- deleted so a re-import cannot resurrect a link a human rejected, and so
  -- the graph can stop counting them without losing why.
  tombstoned_at       timestamptz NULL,
  created_at          timestamptz NOT NULL DEFAULT now(),
  updated_at          timestamptz NOT NULL DEFAULT now(),

  FOREIGN KEY (workspace_id, owner_user_id)     REFERENCES app_user (workspace_id, id) ON DELETE CASCADE,
  -- The person arm CASCADES: a ghost exists to point at somebody, and erasure
  -- already deletes a subject's ghosts outright. SET NULL cannot work here —
  -- clearing matched_person_id on a CONFIRMED row violates the shape CHECK
  -- below, so deleting the person would fail instead.
  FOREIGN KEY (workspace_id, matched_person_id) REFERENCES person (workspace_id, id) ON DELETE CASCADE,
  -- The ACCOUNT arm nulls its own column: a ghost outlives the account it was
  -- placed against, and no CHECK depends on that column.
  FOREIGN KEY (workspace_id, matched_org_id) REFERENCES organization (workspace_id, id) ON DELETE SET NULL (matched_org_id),
  -- A confirmed match must name what it matched. A status that claims a link
  -- it does not hold is worse than no status.
  CONSTRAINT linkedin_connection_match_shape CHECK (
    match_status <> 'confirmed' OR matched_person_id IS NOT NULL
  )
);

-- Re-importing the same export must update rather than duplicate. The
-- provider id is identity when it exists; otherwise this is an explicit
-- BEST-EFFORT dedupe key and not an identity claim — connected_on is in it
-- precisely because two same-named people at one company almost certainly did
-- not connect on the same day.
CREATE UNIQUE INDEX uq_linkedin_connection_provider ON linkedin_connection
  (workspace_id, owner_user_id, provider_member_ref)
  WHERE provider_member_ref IS NOT NULL;
CREATE UNIQUE INDEX uq_linkedin_connection_natural ON linkedin_connection
  (workspace_id, owner_user_id, normalized_name, coalesce(normalized_company, ''), coalesce(connected_on, 'epoch'::date))
  WHERE provider_member_ref IS NULL;

-- The matcher's two lookups, and the org-level reach query.
CREATE INDEX idx_linkedin_connection_email ON linkedin_connection (workspace_id, lower(email))
  WHERE email IS NOT NULL AND tombstoned_at IS NULL;
CREATE INDEX idx_linkedin_connection_match ON linkedin_connection (workspace_id, normalized_name, normalized_company)
  WHERE tombstoned_at IS NULL;
CREATE INDEX idx_linkedin_connection_org ON linkedin_connection (workspace_id, matched_org_id)
  WHERE matched_org_id IS NOT NULL AND tombstoned_at IS NULL;

ALTER TABLE linkedin_connection ENABLE ROW LEVEL SECURITY;
ALTER TABLE linkedin_connection FORCE ROW LEVEL SECURITY;
CREATE POLICY linkedin_connection_tenant_isolation ON linkedin_connection
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

GRANT SELECT, INSERT, UPDATE, DELETE ON linkedin_connection TO margince_app;
