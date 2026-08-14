-- 0246: the agent tables drop the tenant column (ADR-0091 §8 phase D).
--
-- Held back until now because the scheduler fanned out one job per live tenant
-- and its suite injected a fault through a trigger keyed on NEW.workspace_id.
-- The pass is one job now (ADR-0103 §1), so the column has nothing left to
-- hold.

DROP INDEX idx_runner_job_due;
CREATE INDEX idx_runner_job_due ON runner_job (status, due_at);

DROP INDEX idx_agent_run_awaiting;
CREATE INDEX idx_agent_run_awaiting ON agent_run (approval_id) WHERE status = 'awaiting_approval';

DROP INDEX idx_agent_task_expiry;
CREATE INDEX idx_agent_task_expiry ON agent_task (expires_at);

DROP INDEX idx_agent_task_passport;
CREATE INDEX idx_agent_task_passport ON agent_task (passport_id);

ALTER TABLE agent_run DROP CONSTRAINT uq_agent_run_ws_id;

ALTER TABLE agent_run DROP COLUMN workspace_id;
ALTER TABLE runner_job DROP COLUMN workspace_id;
ALTER TABLE agent_task DROP COLUMN workspace_id;
