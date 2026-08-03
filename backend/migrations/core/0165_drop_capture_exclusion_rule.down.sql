-- Recreate the table 0076 defined, so a rollback lands on the schema the code
-- at that revision expects. The rules themselves are gone — a DROP takes its
-- rows with it — so this restores the shape, never the content.

CREATE TABLE IF NOT EXISTS capture_exclusion_rule (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  user_id      uuid NOT NULL,
  kind         text NOT NULL CHECK (kind IN ('sender_domain','recipient_domain','label')),
  value        text NOT NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  archived_at  timestamptz NULL,
  CONSTRAINT capture_exclusion_rule_unique UNIQUE (workspace_id, user_id, kind, value),
  CONSTRAINT capture_exclusion_rule_user_fkey FOREIGN KEY (workspace_id, user_id)
    REFERENCES app_user (workspace_id, id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_capture_exclusion_rule ON capture_exclusion_rule (workspace_id, user_id)
  WHERE archived_at IS NULL;

ALTER TABLE capture_exclusion_rule ENABLE ROW LEVEL SECURITY;
ALTER TABLE capture_exclusion_rule FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS capture_exclusion_rule_tenant_isolation ON capture_exclusion_rule;
CREATE POLICY capture_exclusion_rule_tenant_isolation ON capture_exclusion_rule
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
