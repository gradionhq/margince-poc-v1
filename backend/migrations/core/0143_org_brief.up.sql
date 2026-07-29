-- The standing account brief's cache: one brief per (user, organization).
--
-- Per USER, not per organization. The brief is assembled by running the
-- account's reads as the requesting caller, so it can only describe records
-- that caller may open. A single shared row would either leak scoped deals
-- and activities to a restricted reader, or degrade to the lowest common
-- scope and tell the account owner less than the page already shows them.
-- (The capture_digest table, migration 0098, is the same per-user shape.)
--
-- fingerprint is a hash over the assembled INPUT plus the prompt, task and
-- model-routing versions — never the organization's row version. Facts,
-- deals and activities all move without touching that row, so a key derived
-- from it would serve a stale brief indefinitely.
--
-- Like user_record_view (0142) this is a read-model table: derived content,
-- regenerable at any time, readable by nobody but its own user. It carries
-- no audit row and no outbox event.

CREATE TABLE org_brief (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id  uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  user_id       uuid NOT NULL,
  organization_id uuid NOT NULL,
  fingerprint   text NOT NULL,
  generated_at  timestamptz NOT NULL,
  generated_by  text NOT NULL CHECK (generated_by IN ('model','deterministic')),
  payload       jsonb NOT NULL,
  UNIQUE (workspace_id, user_id, organization_id),
  -- Composite references: a brief can only name a user and an account of its
  -- own workspace, and it goes when either does.
  CONSTRAINT org_brief_user_id_fkey FOREIGN KEY (workspace_id, user_id)
    REFERENCES app_user (workspace_id, id) ON DELETE CASCADE,
  CONSTRAINT org_brief_org_fkey FOREIGN KEY (workspace_id, organization_id)
    REFERENCES organization (workspace_id, id) ON DELETE CASCADE
);

ALTER TABLE org_brief ENABLE ROW LEVEL SECURITY;
ALTER TABLE org_brief FORCE ROW LEVEL SECURITY;
CREATE POLICY org_brief_tenant_isolation ON org_brief
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
