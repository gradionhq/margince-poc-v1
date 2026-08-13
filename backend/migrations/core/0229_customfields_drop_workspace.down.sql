-- Reverse of 0229: the catalog carries the tenant column again.
--
-- The backfill reads `workspace` rather than reconstructing each row's original
-- tenant, because there is only one to read: 0217's pre-flight refuses to run
-- against a database holding more than one live workspace, and ADR-0061 §3 has
-- the API refusing to start in that state. `ORDER BY created_at LIMIT 1` is
-- determinism about which row of a one-row table, not a choice among tenants.
--
-- If `workspace` is empty and the catalog is not, SET NOT NULL fails and the
-- rollback stops — the honest outcome, since no value this migration could
-- write would be true.

ALTER TABLE custom_field ADD COLUMN workspace_id uuid;

UPDATE custom_field SET workspace_id = (SELECT id FROM workspace ORDER BY created_at LIMIT 1);

ALTER TABLE custom_field ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE custom_field ADD CONSTRAINT custom_field_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;

DROP INDEX idx_custom_field_object;
CREATE INDEX idx_custom_field_object ON custom_field (workspace_id, object, status);
