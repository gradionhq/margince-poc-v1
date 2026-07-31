-- 0150: the durable consent behind a remote MCP connection — the grant the
-- human approved once, the rotating refresh tokens minted under it, and the
-- client-lifecycle columns that let an admin switch a connection off.
--
-- 0025 shipped the authorization server with codes and passports only, so a
-- connector's authority lived entirely in a passport with no record of the
-- consent that produced it: nothing tied an access token back to the client
-- and the human who approved it, and therefore nothing could revoke a whole
-- connection. oauth_grant is that record — one row per approved consent,
-- carrying the granted scopes, the RFC 8707 audience, and the offline_access
-- marker that decides whether refresh is allowed at all.
--
-- refresh_allowed rather than storing offline_access in scopes: scopes are
-- passport scopes, checked against the human's RBAC at every bind, and
-- offline_access is not a permission over any record — it is a property of
-- the grant. Keeping it out of the array means no RBAC path has to learn to
-- ignore a pseudo-scope.

CREATE TABLE oauth_grant (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  client_id    text NOT NULL,
  user_id      uuid NOT NULL,
  scopes       text[] NOT NULL,           -- passport scopes only; offline_access never lands here
  refresh_allowed boolean NOT NULL DEFAULT false,   -- the offline_access marker
  resource     text NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  revoked_at   timestamptz NULL,
  CONSTRAINT oauth_grant_ws_id_key UNIQUE (workspace_id, id),   -- the composite FK target
  CONSTRAINT oauth_grant_user_fkey FOREIGN KEY (workspace_id, user_id)
    REFERENCES app_user (workspace_id, id) ON DELETE CASCADE,
  CONSTRAINT oauth_grant_client_fkey FOREIGN KEY (workspace_id, client_id)
    REFERENCES oauth_client (workspace_id, client_id) ON DELETE RESTRICT
);

-- Refresh tokens are stored hashed, like every other bearer credential here,
-- and rotate on each use: consumed_at plus replaced_by make the chain
-- inspectable, so a token presented after consumption can be told apart from
-- a first presentation and answered according to the reuse rule rather than
-- silently accepted.
CREATE TABLE oauth_refresh_token (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  grant_id     uuid NOT NULL,
  token_hash   text NOT NULL,
  expires_at   timestamptz NOT NULL,
  consumed_at  timestamptz NULL,
  replaced_by  uuid NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT oauth_refresh_unique UNIQUE (workspace_id, token_hash),
  CONSTRAINT oauth_refresh_grant_fkey FOREIGN KEY (workspace_id, grant_id)
    REFERENCES oauth_grant (workspace_id, id) ON DELETE CASCADE
);

ALTER TABLE passport ADD COLUMN oauth_grant_id uuid NULL;
-- ON DELETE RESTRICT, and never SET NULL: SET NULL would detach a live
-- passport from a deleted grant and leave that passport valid for the rest of
-- its lifetime — up to 30 days of authority whose consent record no longer
-- exists. Nothing may orphan a live credential, so revoking the passports is
-- the application's explicit first step and the database refuses the delete
-- until it happens. A later migration that relaxes this to SET NULL for the
-- convenience of deleting a grant re-opens exactly that hole.
ALTER TABLE passport ADD CONSTRAINT passport_grant_fkey
  FOREIGN KEY (workspace_id, oauth_grant_id)
  REFERENCES oauth_grant (workspace_id, id) ON DELETE RESTRICT;
-- passport.last_used_at backs the Settings list's "last used" column, which the
-- contract already declares (PassportSummary.last_used_at). The column lands
-- with this DDL and nothing writes it yet: its writer — a stamp on the
-- authenticated /mcp path, debounced to at most one update per passport per
-- minute because it is a write on the hot path — arrives with the per-workspace
-- admin surface. Until then every row reads NULL and the API answers the field
-- as absent.
ALTER TABLE passport ADD COLUMN last_used_at timestamptz NULL;

-- Client lifecycle. Disable is reversible, delete is not, and delete is SOFT:
-- a hard row delete cannot express "revoke every passport and refresh token
-- under this client first" atomically, would fight the RESTRICT above, and
-- would take the audit trail of the connection with it. Every statement that
-- reads oauth_client carries both columns already (identity's
-- liveClientPredicate — issuance and authentication alike). Nothing in the
-- application SETS them yet, so an operator disabling a client does it in raw
-- SQL today; the admin client screen that will — PATCH disabling, DELETE
-- soft-deleting and running the revoke cascade — arrives with the per-workspace
-- admin surface.
ALTER TABLE oauth_client ADD COLUMN disabled_at  timestamptz NULL;
ALTER TABLE oauth_client ADD COLUMN deleted_at   timestamptz NULL;
-- created_via separates a dynamically registered client from one an admin
-- entered, for the consent page's unverified-client warning: anyone may
-- register through DCR, so the human approving one has to be told. Existing
-- rows are 'dcr' by construction — dynamic registration was the only way a
-- client row could come into being before this migration.
ALTER TABLE oauth_client ADD COLUMN created_via  text NOT NULL DEFAULT 'dcr' CHECK (created_via IN ('dcr','admin'));
-- Claude registers a fresh client on every new connection, so an installation
-- accumulates client rows. oauth_client.last_used_at is what the admin client
-- list sorts on to make the never-used ones deletable in bulk; that is the one
-- capability it serves.
ALTER TABLE oauth_client ADD COLUMN last_used_at timestamptz NULL;

-- Tenant tables ⇒ RLS, same deny-on-unset policy as every other
-- (the coverage fitness test refuses a workspace_id table without it).
ALTER TABLE oauth_grant ENABLE ROW LEVEL SECURITY;
ALTER TABLE oauth_grant FORCE ROW LEVEL SECURITY;
CREATE POLICY oauth_grant_tenant_isolation ON oauth_grant
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
ALTER TABLE oauth_refresh_token ENABLE ROW LEVEL SECURITY;
ALTER TABLE oauth_refresh_token FORCE ROW LEVEL SECURITY;
CREATE POLICY oauth_refresh_token_tenant_isolation ON oauth_refresh_token
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

-- Revoking a grant walks every refresh row beneath it, and the composite FK
-- above cascades on grant delete. Without this index both are a sequential
-- scan of every refresh token in the installation.
CREATE INDEX oauth_refresh_token_grant_ix ON oauth_refresh_token (workspace_id, grant_id);
-- The same walk, one table over: the cascade retires every passport under a
-- grant, and so does EVERY rotation (the predecessor dies before its
-- replacement is minted), which makes this the hotter of the two. The existing
-- idx_passport_obo is on (workspace_id, on_behalf_of) and cannot serve it.
CREATE INDEX passport_oauth_grant_ix ON passport (workspace_id, oauth_grant_id);
