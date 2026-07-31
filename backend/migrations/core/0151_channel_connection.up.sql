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
  -- There is no half-connected state. Ingress PULLS (getUpdates), so connect
  -- makes no provider call after the insert and therefore has no window in
  -- which a written row is not yet live: it succeeds as 'connected' or it
  -- writes nothing at all. 'error' and 'reauth_required' are what the poller
  -- parks a connection under; the due-scan selects only 'connected', so the
  -- status IS the enable flag.
  status             text NOT NULL
                       CHECK (status IN ('connected','disconnected','error','reauth_required')),
  -- The getUpdates cursor. getUpdates(offset = poll_offset) is ALSO the
  -- acknowledgement of everything below it, so this advances only in the same
  -- transaction that made the batch durable. 0 means "never polled".
  poll_offset        bigint NOT NULL DEFAULT 0,
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

-- ONE live bot per workspace, not one per (workspace, bot). Two live rows make
-- every outbound reply ambiguous, and the send resolver refuses rather than
-- guessing which bot a customer opened their chat with — so permitting a second
-- binding does not add a capability, it removes the ability to reply at all.
CREATE UNIQUE INDEX uq_channel_connection_ws
  ON channel_connection (workspace_id, provider) WHERE archived_at IS NULL;
-- And ONE installation per bot, globally. Only one getUpdates consumer may hold a
-- bot at a time: a second installation polling the same token does not split the
-- traffic, it steals it — Telegram answers one consumer and refuses the other with
-- a 409, and which of the two wins is a race. No local constraint can see another
-- installation, but this one at least rules out the same fleet doing it to itself.
CREATE UNIQUE INDEX uq_channel_connection_bot
  ON channel_connection (provider, channel_id) WHERE archived_at IS NULL;

-- `version` on this table means "the BINDING moved": the send path resolves a
-- credential, walks the seat/consent/pacing gates, then re-reads this version to
-- refuse a token whose bot was replaced under it, and the lifecycle writers use
-- it as an optimistic guard. An advancing poll cursor is not a change to the
-- binding: bumping on it would fire that fence on every inbound message, and make
-- an admin's disconnect 409 because a customer happened to write mid-request.
--
-- So the bump is skipped when the cursor is the ONLY column that moved. The
-- condition is DERIVED from the row rather than naming the columns it must not
-- skip for: a write that changes poll_offset TOGETHER with the bot it points at
-- (ReplaceToken does exactly that) still bumps, and a column added later is
-- covered without anyone remembering to add it here.
CREATE TRIGGER trg_channel_connection_updated BEFORE UPDATE ON channel_connection
  FOR EACH ROW
  WHEN (to_jsonb(OLD) - 'poll_offset' IS DISTINCT FROM to_jsonb(NEW) - 'poll_offset')
  EXECUTE FUNCTION set_updated_at_bump_version();

ALTER TABLE channel_connection ENABLE ROW LEVEL SECURITY;
ALTER TABLE channel_connection FORCE ROW LEVEL SECURITY;
CREATE POLICY channel_connection_tenant_isolation ON channel_connection
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
