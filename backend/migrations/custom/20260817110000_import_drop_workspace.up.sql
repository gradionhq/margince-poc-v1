-- 20260817110000: the importer drops the tenant column (ADR-0091 §8 phase D).
--
-- import_run and import_record_map are fork-owned (ADR-0017), which is why this
-- half lives here and the AI trace's half lives in core/0284 — the same change,
-- split by who owns the table rather than by what it does.
--
-- import_record_map is the identity ledger a re-import reads to decide whether
-- it has seen an external id before. Its key is already
-- (source_system, object, external_id): the incumbent's own identity, which is
-- what makes a second run of the same connector idempotent.

ALTER TABLE import_record_map DROP CONSTRAINT import_record_map_workspace_id_fkey;
ALTER TABLE import_record_map DROP COLUMN workspace_id;

ALTER TABLE import_run DROP CONSTRAINT import_run_workspace_id_fkey;
ALTER TABLE import_run DROP COLUMN workspace_id;

-- Both indexes led with the column; recreated on what actually selects rows.
CREATE INDEX idx_import_record_map_run ON import_record_map (import_run_id);
CREATE INDEX idx_import_run_ws ON import_run (status);
