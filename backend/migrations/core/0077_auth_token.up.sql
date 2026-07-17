-- 0077: auth_token — single-use emailed credentials (A74/ADR-0056;
-- AUTH-DDL-1). Password reset first; the same table carries email
-- verification and invite activation when those flows land. Only the
-- token's hash persists (the no-raw-credential-at-rest rule the session
-- and passport tables set); the plaintext reaches the user by email and
-- nowhere else. Rows are single-use: redemption stamps used_at, and a
-- used or expired token is refused with one neutral error.

CREATE TABLE auth_token (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  user_id      uuid NOT NULL,
  purpose      text NOT NULL CHECK (purpose IN ('password_reset','email_verify','invite')),
  token_hash   text NOT NULL,              -- SHA-256(raw token); the raw token never touches the DB
  expires_at   timestamptz NOT NULL,       -- short TTL (reset ~1h; verify ~24h; invite ~7d)
  used_at      timestamptz NULL,           -- single-use: set on redemption
  created_at   timestamptz NOT NULL DEFAULT now(),

  -- Same-workspace guarantee (C4, the 0019 composite-FK pattern): the
  -- token's owner must live in this row's workspace.
  CONSTRAINT auth_token_user_fkey FOREIGN KEY (workspace_id, user_id)
    REFERENCES app_user (workspace_id, id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX idx_auth_token_hash ON auth_token (token_hash);
CREATE INDEX idx_auth_token_user ON auth_token (workspace_id, user_id, purpose) WHERE used_at IS NULL;

ALTER TABLE auth_token ENABLE ROW LEVEL SECURITY;
ALTER TABLE auth_token FORCE ROW LEVEL SECURITY;
CREATE POLICY auth_token_tenant_isolation ON auth_token
  USING      (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
