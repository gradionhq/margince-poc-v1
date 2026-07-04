-- 0029: workflow run idempotency (interfaces.md §5 AC-W3): one row per
-- (handler, idempotency key) claims the run — an at-least-once bus
-- redelivery finds the claim and does nothing. The row doubles as the
-- replayable run record (planned vs applied actions).
CREATE TABLE workflow_run (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id    uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  handler         text NOT NULL,
  idempotency_key text NOT NULL,
  trigger_event   uuid NOT NULL,
  status          text NOT NULL DEFAULT 'applied' CHECK (status IN ('applied','skipped','failed','requires_approval')),
  planned         jsonb NOT NULL,
  applied         jsonb NULL,
  error           text NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT workflow_run_unique UNIQUE (workspace_id, handler, idempotency_key)
);

ALTER TABLE workflow_run ENABLE ROW LEVEL SECURITY;
ALTER TABLE workflow_run FORCE ROW LEVEL SECURITY;
CREATE POLICY workflow_run_tenant_isolation ON workflow_run
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
