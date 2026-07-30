-- channel_connection (telegram-oa design §4.1, owned by capture): one
-- workspace-level bot binding per row. capture_connection cannot host this
-- — its user_id is NOT NULL (inherited from granted_by, 0023_capture.up.sql:
-- 20-37, renamed in 0078:8-10 without becoming nullable) because it models
-- one human's grant of one connector, whereas a Telegram bot is connected
-- by an admin on behalf of the whole workspace, not bound to that admin's
-- own account.

CREATE TABLE channel_connection (
  id                 uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id       uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  provider           text NOT NULL CHECK (provider IN ('telegram')),
  channel_id         text NOT NULL,   -- the bot's numeric id, from getMe
  channel_label      text NOT NULL,   -- @username, display only — mutable and re-assignable
  credential_ref     text NOT NULL,   -- vault reference to the bot token
  webhook_secret_ref text NOT NULL,   -- vault reference to the minted secret
  -- pending: the row exists so setWebhook has a target to register against,
  -- but the webhook call has not yet succeeded (§5 Connect) — a pending row
  -- is never treated as live by ingress or send.
  status             text NOT NULL DEFAULT 'pending'
                       CHECK (status IN ('pending','connected','disconnected','error','reauth_required')),
  connected_by       uuid NOT NULL,   -- the admin who ran Connect; audit only
  version            bigint NOT NULL DEFAULT 1,
  created_at         timestamptz NOT NULL DEFAULT now(),
  updated_at         timestamptz NOT NULL DEFAULT now(),
  archived_at        timestamptz NULL,

  -- Composite tenant FK (0019 pattern, TestFK_tenantLocalReferencesAreComposite):
  -- the connecting admin must live in the SAME workspace as the connection.
  CONSTRAINT channel_connection_connected_by_fkey FOREIGN KEY (workspace_id, connected_by)
    REFERENCES app_user (workspace_id, id) ON DELETE RESTRICT
);

CREATE UNIQUE INDEX uq_channel_connection_ws
  ON channel_connection (workspace_id, provider, channel_id) WHERE archived_at IS NULL;
-- Telegram permits exactly ONE webhook URL per bot. Without a GLOBAL constraint the same
-- bot connected in a second workspace silently redirects every delivery away from the
-- first, which goes on reading 'connected' and simply falls quiet — indistinguishable
-- from a healthy channel nobody is messaging.
CREATE UNIQUE INDEX uq_channel_connection_bot
  ON channel_connection (provider, channel_id) WHERE archived_at IS NULL;

CREATE TRIGGER trg_channel_connection_updated BEFORE UPDATE ON channel_connection
  FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

ALTER TABLE channel_connection ENABLE ROW LEVEL SECURITY;
ALTER TABLE channel_connection FORCE ROW LEVEL SECURITY;
CREATE POLICY channel_connection_tenant_isolation ON channel_connection
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
