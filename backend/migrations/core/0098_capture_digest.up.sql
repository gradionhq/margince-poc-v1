-- CAP-DDL-6 (ADR-0063): the morning digest — one per user per day, built by
-- the nightly suite's last pass. The payload is the pre-assembled read
-- (capture totals, review counts, connector health strip): the GET is one
-- indexed row, and every number in it was counted from persisted rows at
-- build time.

CREATE TABLE capture_digest (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  user_id      uuid NOT NULL,
  digest_date  date NOT NULL,
  payload      jsonb NOT NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  UNIQUE (workspace_id, user_id, digest_date),
  -- Composite reference: a digest can only name a user of its own workspace.
  CONSTRAINT capture_digest_user_id_fkey FOREIGN KEY (workspace_id, user_id)
    REFERENCES app_user (workspace_id, id) ON DELETE CASCADE
);

ALTER TABLE capture_digest ENABLE ROW LEVEL SECURITY;
ALTER TABLE capture_digest FORCE ROW LEVEL SECURITY;
CREATE POLICY capture_digest_tenant_isolation ON capture_digest
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
