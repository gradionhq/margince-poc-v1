-- PO-DDL-12 (ADR-0063): the signature-enrichment evidence sidecar — the
-- person-side mirror of organization_profile_field. Every accepted field
-- (column-backed or not) upserts one row here so the value is auditable
-- back to the verbatim signature line; field_provenance rides the same
-- commit and deliberately does NOT carry the evidence text — this does.

CREATE TABLE person_profile_field (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id    uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  person_id       uuid NOT NULL,
  field           text NOT NULL CHECK (field IN ('title','phone','role','linkedin','org_name')),
  value           text NOT NULL,
  evidence_snippet text NOT NULL,               -- verbatim signature text (evidence-or-omit: never nullable here — a field with no snippet is dropped before write)
  source_ref      text NOT NULL,                -- the source activity: 'activity:<uuid>' — the mail the signature came from
  confidence      numeric(4,3) NULL CHECK (confidence IS NULL OR confidence BETWEEN 0 AND 1),
  source          text NOT NULL,                -- 'capture_enrich'
  captured_by     text NOT NULL,                -- 'agent:enrich'; 'human:*' once a human edits the field
  version         bigint NOT NULL DEFAULT 1,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT person_profile_field_person_fk FOREIGN KEY (workspace_id, person_id) REFERENCES person (workspace_id, id) ON DELETE CASCADE,
  CONSTRAINT uq_person_profile_field UNIQUE (person_id, field)   -- one row per (person, field)
);
CREATE INDEX idx_person_profile_field ON person_profile_field (workspace_id, person_id);

CREATE TRIGGER trg_person_profile_field_updated BEFORE UPDATE ON person_profile_field
  FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

ALTER TABLE person_profile_field ENABLE ROW LEVEL SECURITY;
ALTER TABLE person_profile_field FORCE ROW LEVEL SECURITY;
CREATE POLICY person_profile_field_tenant_isolation ON person_profile_field
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
