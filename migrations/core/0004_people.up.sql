-- People (data-model §3.1–§3.3). person.converted_from_lead_id is added in
-- 0008_leads (the lead table does not exist yet; lead ↔ person FKs are
-- mutually referencing, so the later migration closes the loop).
-- The version column on mutable domain tables comes from §1.3a; the §3 DDL
-- blocks predate it (logged as fable feedback).

CREATE TABLE person (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id  uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  first_name    text NULL,
  last_name     text NULL,
  full_name     text NOT NULL,           -- always present (display); split names optional
  title         text NULL,               -- denormalized display title; authoritative title is on relationship (§5)
  owner_id      uuid NULL REFERENCES app_user(id) ON DELETE SET NULL,
  social        jsonb NOT NULL DEFAULT '{}'::jsonb,
  address       jsonb NULL,

  merged_into_id uuid NULL REFERENCES person(id) ON DELETE SET NULL, -- set when this row was merged AWAY

  source        text NOT NULL,
  captured_by   text NOT NULL,
  raw           jsonb NULL,

  search_tsv    tsvector GENERATED ALWAYS AS (
                  to_tsvector('simple',
                    coalesce(full_name,'') || ' ' || coalesce(title,''))
                ) STORED,

  version       bigint NOT NULL DEFAULT 1,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  archived_at   timestamptz NULL
);

CREATE INDEX idx_person_ws_live     ON person (workspace_id) WHERE archived_at IS NULL;
CREATE INDEX idx_person_owner       ON person (workspace_id, owner_id) WHERE archived_at IS NULL;
CREATE INDEX idx_person_search      ON person USING gin (search_tsv);
CREATE INDEX idx_person_merged_into ON person (merged_into_id) WHERE merged_into_id IS NOT NULL;
CREATE TRIGGER trg_person_updated BEFORE UPDATE ON person
  FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

CREATE TABLE person_email (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  person_id    uuid NOT NULL REFERENCES person(id) ON DELETE CASCADE,
  email        text NOT NULL,                 -- stored lowercased
  email_type   text NOT NULL DEFAULT 'work' CHECK (email_type IN ('work','personal','other')),
  is_primary   boolean NOT NULL DEFAULT false,
  position     integer NOT NULL DEFAULT 0,
  source       text NOT NULL,
  captured_by  text NOT NULL,
  version      bigint NOT NULL DEFAULT 1,
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  archived_at  timestamptz NULL,
  CONSTRAINT person_email_norm CHECK (email = lower(email))
);

-- ≤1 primary per (person, type)
CREATE UNIQUE INDEX uq_person_email_primary
  ON person_email (person_id, email_type)
  WHERE is_primary AND archived_at IS NULL;

-- dedupe key: an email is unique across LIVE persons in a workspace → 409 on collision
CREATE UNIQUE INDEX uq_person_email_dedupe
  ON person_email (workspace_id, email)
  WHERE archived_at IS NULL;

CREATE INDEX idx_person_email_person ON person_email (person_id) WHERE archived_at IS NULL;
CREATE TRIGGER trg_person_email_updated BEFORE UPDATE ON person_email
  FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

CREATE TABLE person_phone (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  person_id    uuid NOT NULL REFERENCES person(id) ON DELETE CASCADE,
  phone        text NOT NULL,                  -- E.164 normalized at write
  phone_type   text NOT NULL DEFAULT 'work' CHECK (phone_type IN ('work','mobile','home','other')),
  is_primary   boolean NOT NULL DEFAULT false,
  position     integer NOT NULL DEFAULT 0,
  source       text NOT NULL,
  captured_by  text NOT NULL,
  version      bigint NOT NULL DEFAULT 1,
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  archived_at  timestamptz NULL
);
CREATE UNIQUE INDEX uq_person_phone_primary
  ON person_phone (person_id, phone_type)
  WHERE is_primary AND archived_at IS NULL;
CREATE INDEX idx_person_phone_person ON person_phone (person_id) WHERE archived_at IS NULL;
CREATE TRIGGER trg_person_phone_updated BEFORE UPDATE ON person_phone
  FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();
