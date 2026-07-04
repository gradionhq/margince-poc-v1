-- 0021: Surface-B runner state (architecture/07 §5/§6): agent_run is
-- the durable run record — trace, budget spent, and the suspended
-- window a 🟡 staging parks — and runner_job is the trigger queue
-- (River-shaped semantics on a plain table: durable, idempotent,
-- claimed FOR UPDATE SKIP LOCKED; swapping River in later changes the
-- claim loop, not the rows).

-- agent_run's approval FK rides the composite target key; approval was
-- the one referenced table 0019 never gave one (nothing pointed at it).
ALTER TABLE approval ADD CONSTRAINT uq_approval_ws_id UNIQUE (workspace_id, id);

CREATE TABLE agent_run (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id   uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  agent_spec     text NOT NULL,
  goal           text NOT NULL,
  trigger_ref    text NOT NULL,
  passport_id    uuid NULL,
  status         text NOT NULL DEFAULT 'running'
                   CHECK (status IN ('running','awaiting_approval','completed','degraded','failed')),
  approval_id    uuid NULL,
  -- The §5 suspension snapshot: the staged call + window + budget spent,
  -- everything Resume needs. NULL unless awaiting_approval.
  pending        jsonb NULL,
  result         jsonb NULL,
  -- The §6 replayable record: ordered (proposal → admission → observation).
  trace          jsonb NOT NULL DEFAULT '[]'::jsonb,
  degrade_reason text NULL,
  steps_used     int  NOT NULL DEFAULT 0,
  output_tokens  int  NOT NULL DEFAULT 0,
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),
  finished_at    timestamptz NULL,
  -- One run per trigger occurrence: a retried trigger resumes the
  -- existing run, never starts a duplicate (§6 idempotency).
  CONSTRAINT agent_run_trigger_unique UNIQUE (workspace_id, trigger_ref),
  CONSTRAINT uq_agent_run_ws_id UNIQUE (workspace_id, id),
  CONSTRAINT agent_run_passport_fkey FOREIGN KEY (workspace_id, passport_id)
    REFERENCES passport (workspace_id, id) ON DELETE SET NULL (passport_id),
  CONSTRAINT agent_run_approval_fkey FOREIGN KEY (workspace_id, approval_id)
    REFERENCES approval (workspace_id, id) ON DELETE SET NULL (approval_id),
  -- A suspended run always knows what it is waiting for and how to resume.
  CONSTRAINT agent_run_awaiting_shape CHECK (
    status <> 'awaiting_approval' OR (approval_id IS NOT NULL AND pending IS NOT NULL))
);
CREATE INDEX idx_agent_run_awaiting ON agent_run (workspace_id, approval_id)
  WHERE status = 'awaiting_approval';

CREATE TABLE runner_job (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  agent_spec   text NOT NULL,
  trigger_ref  text NOT NULL,
  -- The authority the run executes under. Cron-seeded jobs carry NULL
  -- until a workspace binds a passport to the spec; execution then
  -- fails loudly instead of running with ambient authority.
  passport_id  uuid NULL,
  due_at       timestamptz NOT NULL,
  status       text NOT NULL DEFAULT 'queued'
                 CHECK (status IN ('queued','running','done','failed')),
  attempts     int  NOT NULL DEFAULT 0,
  last_error   text NULL,
  agent_run_id uuid NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  -- Seeding the same trigger occurrence twice is a no-op.
  CONSTRAINT runner_job_trigger_unique UNIQUE (workspace_id, agent_spec, trigger_ref),
  CONSTRAINT runner_job_passport_fkey FOREIGN KEY (workspace_id, passport_id)
    REFERENCES passport (workspace_id, id) ON DELETE SET NULL (passport_id),
  CONSTRAINT runner_job_run_fkey FOREIGN KEY (workspace_id, agent_run_id)
    REFERENCES agent_run (workspace_id, id) ON DELETE SET NULL (agent_run_id)
);
CREATE INDEX idx_runner_job_due ON runner_job (workspace_id, status, due_at);

-- Tenant tables ⇒ RLS, same deny-on-unset policy as every other
-- (the coverage fitness test refuses a workspace_id table without it).
-- The worker crosses tenants by iterating workspaces and binding the
-- GUC per workspace — never by an RLS bypass.
ALTER TABLE agent_run ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent_run FORCE ROW LEVEL SECURITY;
CREATE POLICY agent_run_tenant_isolation ON agent_run
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
ALTER TABLE runner_job ENABLE ROW LEVEL SECURITY;
ALTER TABLE runner_job FORCE ROW LEVEL SECURITY;
CREATE POLICY runner_job_tenant_isolation ON runner_job
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
