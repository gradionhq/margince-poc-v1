-- B-E02.12: field-level provenance — the single shared child table the
-- provenance display reads (gate Q1→a: normalized rows, NOT per-object
-- jsonb). Row-level source/captured_by on each record stays the
-- creation default (gate Q3→a); a field with no row here falls back to
-- it. Covers every core captured object via object_type (gate Q2→a).
CREATE TABLE field_provenance (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id  uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  object_type   text NOT NULL CHECK (object_type IN ('person','organization','deal','activity','lead')),
  object_id     uuid NOT NULL,
  field_name    text NOT NULL,
  source        text NOT NULL,
  captured_by   text NOT NULL,
  captured_at   timestamptz NOT NULL DEFAULT now(),
  confidence    real NULL CHECK (confidence IS NULL OR (confidence >= 0 AND confidence <= 1)),
  evidence_ref  text NULL
);

-- The display reads one record's field origins; the latest row per
-- field wins, so the lookup is (object, field, recency).
CREATE INDEX idx_field_provenance_object
  ON field_provenance (workspace_id, object_type, object_id, field_name, captured_at DESC);

ALTER TABLE field_provenance ENABLE ROW LEVEL SECURITY;
ALTER TABLE field_provenance FORCE ROW LEVEL SECURITY;
CREATE POLICY field_provenance_tenant_isolation ON field_provenance
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
