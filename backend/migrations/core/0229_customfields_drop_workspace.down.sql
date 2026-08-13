-- Reverse of 0229: the catalog carries the tenant column again.
--
-- The backfill reads the LIVE workspace, and the predicate is the point: 0217's
-- pre-flight refuses to run against a database holding more than one workspace
-- with archived_at IS NULL, so there is exactly one live row — but an
-- installation that resolved to one organization by ARCHIVING the others still
-- has those rows, and 0217 names that residue explicitly. Ordering by
-- created_at alone would hand every restored row to whichever workspace
-- happened to be created first, archived or not.
--
-- If `workspace` is empty and the catalog is not, SET NOT NULL fails and the
-- rollback stops — the honest outcome, since no value this migration could
-- write would be true.

ALTER TABLE custom_field ADD COLUMN workspace_id uuid;

UPDATE custom_field SET workspace_id = (SELECT id FROM workspace WHERE archived_at IS NULL ORDER BY created_at LIMIT 1);

ALTER TABLE custom_field ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE custom_field ADD CONSTRAINT custom_field_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;

DROP INDEX idx_custom_field_object;
CREATE INDEX idx_custom_field_object ON custom_field (workspace_id, object, status);
