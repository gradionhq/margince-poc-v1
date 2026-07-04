-- 0025: the A2 OAuth authorization server + workspace signing keys
-- (B-EP06.18b, B-EP03.14/.15, ADR-0013/ADR-0036). The product IS the
-- authorization server: DCR self-registers PUBLIC clients only — the
-- client table has no secret column, so a privileged client cannot
-- exist by construction (PKCE is the proof of possession).

CREATE TABLE oauth_client (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id  uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  client_id     text NOT NULL,
  client_name   text NOT NULL,
  redirect_uris text[] NOT NULL,
  created_at    timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT oauth_client_unique UNIQUE (workspace_id, client_id)
);

CREATE TABLE oauth_authorization_code (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id   uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  code_hash      text NOT NULL,
  client_id      text NOT NULL,
  user_id        uuid NOT NULL,
  scopes         text[] NOT NULL,
  -- S256 only (RFC 7636): the plain method is refused at authorize time.
  code_challenge text NOT NULL,
  redirect_uri   text NOT NULL,
  -- RFC 8707 audience binding: the token minted from this code is only
  -- for this resource.
  resource       text NULL,
  expires_at     timestamptz NOT NULL,
  consumed_at    timestamptz NULL,
  created_at     timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT oauth_code_unique UNIQUE (workspace_id, code_hash),
  CONSTRAINT oauth_code_user_fkey FOREIGN KEY (workspace_id, user_id)
    REFERENCES app_user (workspace_id, id) ON DELETE CASCADE
);

-- Workspace-scoped Ed25519 signing keys (ADR-0036 §1): approval tokens
-- are compact JWS signed per workspace; kid travels in the header so
-- verification survives rotation.
CREATE TABLE workspace_signing_key (
  workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  kid          text NOT NULL,
  alg          text NOT NULL DEFAULT 'EdDSA' CHECK (alg = 'EdDSA'),
  private_key  bytea NOT NULL,
  public_key   bytea NOT NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  retired_at   timestamptz NULL,
  PRIMARY KEY (workspace_id, kid)
);

-- Tenant tables ⇒ RLS, same deny-on-unset policy as every other
-- (the coverage fitness test refuses a workspace_id table without it).
ALTER TABLE oauth_client ENABLE ROW LEVEL SECURITY;
ALTER TABLE oauth_client FORCE ROW LEVEL SECURITY;
CREATE POLICY oauth_client_tenant_isolation ON oauth_client
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
ALTER TABLE oauth_authorization_code ENABLE ROW LEVEL SECURITY;
ALTER TABLE oauth_authorization_code FORCE ROW LEVEL SECURITY;
CREATE POLICY oauth_code_tenant_isolation ON oauth_authorization_code
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
ALTER TABLE workspace_signing_key ENABLE ROW LEVEL SECURITY;
ALTER TABLE workspace_signing_key FORCE ROW LEVEL SECURITY;
CREATE POLICY workspace_signing_key_tenant_isolation ON workspace_signing_key
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
