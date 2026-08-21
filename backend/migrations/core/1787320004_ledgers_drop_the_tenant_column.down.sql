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

-- ADD COLUMN ... DEFAULT, never ADD then UPDATE. These two tables carry a
-- BEFORE UPDATE OR DELETE ... FOR EACH ROW trigger that raises unconditionally
-- (trg_audit_no_mutate and trg_system_log_no_mutate, both in the baseline), so an
-- UPDATE backfill aborts on the first existing row and takes the ADD COLUMN
-- with it — dbmigrate runs each file in one transaction, so the rollback would
-- restore NOTHING and pin the operator at head. ADD COLUMN with a DEFAULT
-- rewrites the rows without issuing row UPDATEs, so the trigger never fires.
--
-- Every other phase D down half is a plain ADD-then-UPDATE. It works there
-- because those tables are mutable; the ledgers are exactly the two where it
-- cannot, which is why this one is spelled differently.
--
-- format() with %L rather than a parameter: a migration is plain SQL with no
-- bind parameters, and the value has to reach a DDL statement.
DO $$
DECLARE ws uuid;
BEGIN
  SELECT id INTO ws FROM workspace WHERE archived_at IS NULL ORDER BY created_at LIMIT 1;
  IF ws IS NULL THEN
    SELECT id INTO ws FROM workspace ORDER BY created_at LIMIT 1;
  END IF;
  IF ws IS NULL THEN
    -- No workspace at all, so no ledger row can exist either: every row in
    -- both tables is written inside a workspace-bound transaction, and the
    -- foreign key restored below would have refused one otherwise. An empty
    -- table takes the column NOT NULL with nothing to backfill.
    ALTER TABLE audit_log  ADD COLUMN workspace_id uuid NOT NULL;
    ALTER TABLE system_log ADD COLUMN workspace_id uuid NOT NULL;
  ELSE
    EXECUTE format('ALTER TABLE audit_log  ADD COLUMN workspace_id uuid NOT NULL DEFAULT %L', ws);
    EXECUTE format('ALTER TABLE system_log ADD COLUMN workspace_id uuid NOT NULL DEFAULT %L', ws);
    -- The default was the backfill's vehicle, not part of the restored shape:
    -- every writer names the column, and leaving a default would let one that
    -- forgot it land on a silently-chosen workspace.
    ALTER TABLE audit_log  ALTER COLUMN workspace_id DROP DEFAULT;
    ALTER TABLE system_log ALTER COLUMN workspace_id DROP DEFAULT;
  END IF;
END $$;

ALTER TABLE audit_log
  ADD CONSTRAINT audit_log_workspace_id_fkey
    FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE system_log
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
