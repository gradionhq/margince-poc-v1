-- Reverse of the phase D drop on the append-only ledgers.
--
-- The column comes back bound to the installation's own LIVE workspace, which
-- is the only one a single-organization installation has (ADR-0061) and
-- therefore the one every restored row belonged to. `archived_at IS NULL`
-- rather than the oldest row: an installation that merged an archived
-- predecessor still carries it, and it can be the older one.
--
-- The shape is restored and the values are not, which is the honest limit of
-- this direction. Nothing else records which workspace a historical action
-- belonged to, and the immutability trigger on both tables means no later pass
-- could repair them either.
SET LOCAL lock_timeout = '3s';

ALTER TABLE audit_log ADD COLUMN workspace_id uuid;
UPDATE audit_log SET workspace_id =
  (SELECT id FROM workspace WHERE archived_at IS NULL ORDER BY created_at LIMIT 1);
ALTER TABLE audit_log
  ALTER COLUMN workspace_id SET NOT NULL,
  ADD CONSTRAINT audit_log_workspace_id_fkey
    FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;

ALTER TABLE system_log ADD COLUMN workspace_id uuid;
UPDATE system_log SET workspace_id =
  (SELECT id FROM workspace WHERE archived_at IS NULL ORDER BY created_at LIMIT 1);
ALTER TABLE system_log
  ALTER COLUMN workspace_id SET NOT NULL,
  ADD CONSTRAINT system_log_workspace_id_fkey
    FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;

CREATE INDEX idx_audit_actor_wide  ON audit_log (workspace_id, actor_id, occurred_at DESC);
CREATE INDEX idx_audit_entity_wide ON audit_log (workspace_id, entity_type, entity_id, occurred_at DESC);
CREATE INDEX idx_audit_time_wide   ON audit_log (workspace_id, occurred_at DESC);
DROP INDEX idx_audit_actor;
DROP INDEX idx_audit_entity;
DROP INDEX idx_audit_time;
ALTER INDEX idx_audit_actor_wide  RENAME TO idx_audit_actor;
ALTER INDEX idx_audit_entity_wide RENAME TO idx_audit_entity;
ALTER INDEX idx_audit_time_wide   RENAME TO idx_audit_time;

CREATE INDEX idx_system_log_action_wide ON system_log (workspace_id, action, occurred_at DESC);
CREATE INDEX idx_system_log_actor_wide  ON system_log (workspace_id, actor_id, occurred_at DESC);
CREATE INDEX idx_system_log_time_wide   ON system_log (workspace_id, occurred_at DESC);
DROP INDEX idx_system_log_action;
DROP INDEX idx_system_log_actor;
DROP INDEX idx_system_log_time;
ALTER INDEX idx_system_log_action_wide RENAME TO idx_system_log_action;
ALTER INDEX idx_system_log_actor_wide  RENAME TO idx_system_log_actor;
ALTER INDEX idx_system_log_time_wide   RENAME TO idx_system_log_time;
