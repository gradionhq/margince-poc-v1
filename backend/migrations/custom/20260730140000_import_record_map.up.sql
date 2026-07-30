-- 20260730140000_import_record_map: the importer's OWN external→native
-- identity map (fork-owned, ADR-0017 custom namespace).
--
-- Why a table rather than the rows' own provenance columns: `source`,
-- `source_system` and `source_id` are client-writable on every create
-- path (a rep may POST any value), so keying the importer's
-- already-landed check on them would let a caller pre-plant a row under
-- an incumbent id and have the flip treat the real estate record as
-- already imported — suppressing it, and capturing every activity that
-- resolves through the same identity. This map is written ONLY by the
-- engine, inside the same transaction-scoped run that landed the row.
CREATE TABLE import_record_map (
  workspace_id  uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  source_system text NOT NULL,          -- the incumbent/connector the id belongs to
  object        text NOT NULL,          -- canonical object class
  external_id   text NOT NULL,          -- the source's own record id
  native_id     uuid NOT NULL,
  import_run_id uuid NOT NULL REFERENCES import_run(id) ON DELETE RESTRICT,
  created_at    timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (workspace_id, source_system, object, external_id)
);
CREATE INDEX idx_import_record_map_run ON import_record_map (workspace_id, import_run_id);

ALTER TABLE import_record_map ENABLE ROW LEVEL SECURITY;
ALTER TABLE import_record_map FORCE ROW LEVEL SECURITY;
CREATE POLICY import_record_map_tenant_isolation ON import_record_map
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
