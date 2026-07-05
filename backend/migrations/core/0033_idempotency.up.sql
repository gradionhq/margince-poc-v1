-- 0033: transport-level POST idempotency (crm.yaml `IdempotencyKey`).
-- The first request under a key claims the row inside the caller's
-- workspace transaction; a replay within the 24h retention window
-- returns the recorded response; the same key with a different request
-- digest is refused (409 idempotency_key_conflict). The key is scoped
-- per (workspace, principal, concrete request path) exactly as the
-- contract's parameter description states.
CREATE TABLE idempotency_key (
  workspace_id    uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  principal_id    text NOT NULL,
  key             text NOT NULL,
  endpoint        text NOT NULL,          -- METHOD + concrete request path
  request_digest  text NOT NULL,          -- sha256 hex of the request body
  response_status int  NULL,              -- NULL while the first request is in flight
  response_body   text NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (workspace_id, principal_id, key, endpoint)
);

ALTER TABLE idempotency_key ENABLE ROW LEVEL SECURITY;
ALTER TABLE idempotency_key FORCE ROW LEVEL SECURITY;
CREATE POLICY idempotency_key_tenant_isolation ON idempotency_key
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

-- The 24h retention sweep: expired claims are re-claimable in place, but
-- an index keeps a future cleanup pass cheap.
CREATE INDEX idx_idempotency_key_created ON idempotency_key (created_at);
