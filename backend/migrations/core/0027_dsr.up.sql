-- 0027: data-subject requests (B-E11.30, data-model §12.5) — Art. 15/16/17
-- requests tracked to completion with the statutory deadline explicit.
CREATE TABLE data_subject_request (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  kind         text NOT NULL CHECK (kind IN ('access','rectify','erasure')),
  status       text NOT NULL DEFAULT 'open' CHECK (status IN ('open','in_progress','fulfilled','rejected')),
  -- The data subject: a person id or an external identifier — a request
  -- can arrive for someone the CRM never captured.
  subject_ref  text NOT NULL,
  assignee_id  uuid NULL,
  due_at       timestamptz NOT NULL,
  resolution   text NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  -- A closed request carries its answer.
  CONSTRAINT dsr_resolution_shape CHECK (status NOT IN ('fulfilled','rejected') OR resolution IS NOT NULL),
  CONSTRAINT dsr_assignee_fkey FOREIGN KEY (workspace_id, assignee_id)
    REFERENCES app_user (workspace_id, id) ON DELETE SET NULL (assignee_id)
);
CREATE INDEX idx_dsr_open ON data_subject_request (workspace_id, due_at) WHERE status IN ('open','in_progress');
CREATE TRIGGER trg_dsr_updated BEFORE UPDATE ON data_subject_request
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Tenant table ⇒ RLS, same deny-on-unset policy as every other
-- (the coverage fitness test refuses a workspace_id table without it).
ALTER TABLE data_subject_request ENABLE ROW LEVEL SECURITY;
ALTER TABLE data_subject_request FORCE ROW LEVEL SECURITY;
CREATE POLICY dsr_tenant_isolation ON data_subject_request
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
