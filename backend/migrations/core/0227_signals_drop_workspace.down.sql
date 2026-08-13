-- Reverse of 0227: the signals tables carry the tenant column again.
--
-- The column comes back nullable, is filled from the installation's single
-- workspace, and only then becomes NOT NULL. The backfill reads `workspace`
-- rather than taking an argument because that row is what the column meant:
-- under ADR-0061 there is exactly one, and a down migration that invented an
-- id would restore the shape while destroying the fact.
--
-- If `workspace` is empty and the tables are not, SET NOT NULL fails and the
-- rollback stops. That is the honest outcome — the rows belonged to a
-- workspace that no longer exists, and no value this migration could write
-- would be true.

ALTER TABLE signal ADD COLUMN workspace_id uuid;
ALTER TABLE signal_resolution ADD COLUMN workspace_id uuid;

UPDATE signal SET workspace_id = (SELECT id FROM workspace ORDER BY created_at LIMIT 1);
UPDATE signal_resolution SET workspace_id = (SELECT id FROM workspace ORDER BY created_at LIMIT 1);

ALTER TABLE signal ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE signal_resolution ALTER COLUMN workspace_id SET NOT NULL;

ALTER TABLE signal ADD CONSTRAINT signal_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE signal_resolution ADD CONSTRAINT signal_resolution_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;

ALTER TABLE signal ADD CONSTRAINT uq_signal_ws_id UNIQUE (id);

DROP INDEX idx_signal_open;
CREATE INDEX idx_signal_open ON signal (workspace_id, status, severity, detected_at DESC);

DROP INDEX idx_signal_unresolved;
CREATE INDEX idx_signal_unresolved ON signal (workspace_id, resolution_state, detected_at DESC);

DROP INDEX signal_resolved_org_ix;
CREATE INDEX signal_resolved_org_ix ON signal (workspace_id, resolved_org_id) WHERE resolved_org_id IS NOT NULL;

DROP INDEX signal_entity_ix;
CREATE INDEX signal_entity_ix ON signal (workspace_id, entity_type, entity_id) WHERE entity_id IS NOT NULL;

DROP INDEX idx_signal_owner_private;
CREATE INDEX idx_signal_owner_private ON signal (workspace_id, owner_id)
  WHERE visibility = 'owner' AND archived_at IS NULL;

DROP INDEX idx_sigres_signal;
CREATE INDEX idx_sigres_signal ON signal_resolution (workspace_id, signal_id, created_at DESC);
