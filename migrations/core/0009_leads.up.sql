-- Leads (data-model §8, ADR-0008): thin, segregated, no organization FK,
-- absent from the relationship graph by construction. This migration also
-- closes the person ↔ lead FK loop left open in 0004_people.

CREATE TABLE lead (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id  uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,

  full_name     text NULL,
  email         text NULL,          -- lowercased; lead-internal dedupe key
  title         text NULL,
  company_name  text NULL,          -- FREE TEXT — NOT an organization FK (ADR-0008 §1)
  candidate_org_key text NULL,      -- loose key for ABM roll-up WITHOUT creating an org row

  status        text NOT NULL DEFAULT 'new' CHECK (status IN ('new','working','promoted','disqualified')),
  score         integer NOT NULL DEFAULT 0,    -- computed from lead-local signals only
  owner_id      uuid NULL REFERENCES app_user(id) ON DELETE SET NULL,

  -- idempotent bulk sourcing: re-import makes no dupes
  source_system text NULL,
  source_id     text NULL,

  promoted_person_id uuid NULL REFERENCES person(id) ON DELETE SET NULL, -- outcome pointer; canonical is person.converted_from_lead_id
  promoted_at        timestamptz NULL,

  source        text NOT NULL,
  captured_by   text NOT NULL,      -- 'agent:sdr' | 'connector:apollo' | 'import:<batch>' | 'human:<id>'
  raw           jsonb NULL,

  search_tsv    tsvector GENERATED ALWAYS AS (
                  to_tsvector('simple', coalesce(full_name,'') || ' ' || coalesce(company_name,'') || ' ' || coalesce(title,''))
                ) STORED,

  version       bigint NOT NULL DEFAULT 1,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  archived_at   timestamptz NULL,

  CONSTRAINT lead_email_norm CHECK (email IS NULL OR email = lower(email))
);
CREATE TRIGGER trg_lead_updated BEFORE UPDATE ON lead
  FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

-- lead-internal exact-email dedupe among LIVE leads → 409 on collision
CREATE UNIQUE INDEX uq_lead_email_dedupe ON lead (workspace_id, email) WHERE email IS NOT NULL AND archived_at IS NULL;
-- idempotent re-import
CREATE UNIQUE INDEX uq_lead_source ON lead (workspace_id, source_system, source_id) WHERE source_system IS NOT NULL AND source_id IS NOT NULL;

CREATE INDEX idx_lead_ws_live  ON lead (workspace_id, status) WHERE archived_at IS NULL;
CREATE INDEX idx_lead_owner    ON lead (workspace_id, owner_id) WHERE archived_at IS NULL;
CREATE INDEX idx_lead_score    ON lead (workspace_id, score DESC) WHERE archived_at IS NULL AND status IN ('new','working');
CREATE INDEX idx_lead_cand_org ON lead (workspace_id, candidate_org_key) WHERE candidate_org_key IS NOT NULL AND archived_at IS NULL;
CREATE INDEX idx_lead_search   ON lead USING gin (search_tsv);

-- The canonical, non-lossy promotion origin pointer (ADR-0008 §4).
ALTER TABLE person
  ADD COLUMN converted_from_lead_id uuid NULL REFERENCES lead(id) ON DELETE SET NULL;
CREATE INDEX idx_person_from_lead ON person (converted_from_lead_id) WHERE converted_from_lead_id IS NOT NULL;
