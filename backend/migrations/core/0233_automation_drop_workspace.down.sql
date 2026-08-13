-- Reverse of 0233: the catalog carries the tenant column again.
--
-- The backfill reads `workspace` because there is exactly one to read: 0217's
-- pre-flight refuses to run against a database holding more than one live
-- workspace, and ADR-0061 §3 has the API refusing to start in that state. If
-- `workspace` is empty and the catalog is not, SET NOT NULL fails and the
-- rollback stops — the honest outcome, since no value this migration could
-- write would be true.

ALTER TABLE automation ADD COLUMN workspace_id uuid;

UPDATE automation SET workspace_id = (SELECT id FROM workspace ORDER BY created_at LIMIT 1);

ALTER TABLE automation ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE automation ADD CONSTRAINT automation_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE automation ADD CONSTRAINT uq_automation_ws_id UNIQUE (id);

DROP INDEX idx_automation_key_live;
CREATE INDEX idx_automation_ws_key_live ON automation (workspace_id, key) WHERE enabled AND archived_at IS NULL;
