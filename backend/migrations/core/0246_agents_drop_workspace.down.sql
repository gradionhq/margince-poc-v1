-- Reverse of 0246: the three agent tables carry the tenant column again.
--
-- The backfill reads the LIVE workspace, and the predicate is the point: 0217's
-- pre-flight refuses to run against a database holding more than one workspace
-- with archived_at IS NULL, so there is exactly one live row — but an
-- installation that resolved to one organization by ARCHIVING the others still
-- has those rows, and 0217 names that residue explicitly. Ordering by
-- created_at alone would hand every restored row to whichever workspace
-- happened to be created first, archived or not.
--
-- If no live workspace exists and a table is not empty, SET NOT NULL fails and
-- the rollback stops — the honest outcome, since no value this migration could
-- write would be true.

ALTER TABLE agent_run ADD COLUMN workspace_id uuid;
ALTER TABLE runner_job ADD COLUMN workspace_id uuid;
ALTER TABLE agent_task ADD COLUMN workspace_id uuid;

DO $$
DECLARE ws uuid := (SELECT id FROM workspace WHERE archived_at IS NULL ORDER BY created_at LIMIT 1);
BEGIN
  UPDATE agent_run SET workspace_id = ws;
  UPDATE runner_job SET workspace_id = ws;
  UPDATE agent_task SET workspace_id = ws;
END $$;

ALTER TABLE agent_run ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE runner_job ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE agent_task ALTER COLUMN workspace_id SET NOT NULL;

ALTER TABLE agent_run ADD CONSTRAINT agent_run_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE runner_job ADD CONSTRAINT runner_job_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE agent_task ADD CONSTRAINT agent_task_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;

ALTER TABLE agent_run ADD CONSTRAINT uq_agent_run_ws_id UNIQUE (id);

DROP INDEX idx_runner_job_due;
CREATE INDEX idx_runner_job_due ON runner_job (workspace_id, status, due_at);

DROP INDEX idx_agent_run_awaiting;
CREATE INDEX idx_agent_run_awaiting ON agent_run (workspace_id, approval_id) WHERE status = 'awaiting_approval';

DROP INDEX idx_agent_task_expiry;
CREATE INDEX idx_agent_task_expiry ON agent_task (workspace_id, expires_at);

DROP INDEX idx_agent_task_passport;
CREATE INDEX idx_agent_task_passport ON agent_task (workspace_id, passport_id);
