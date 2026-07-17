-- 0078: reconcile the improvised connector_connection into capture_connection
-- (capture.md CAP-DDL-2). Rename-in-place preserves every row, the vault
-- credential_ref, and the legacy auth bytea through the additive transition.
-- The watch-renewal column + its partial index land here so the Gmail Pub/Sub
-- push-watch follow-up can key on watch_expires_at; the watch BEHAVIOUR is a
-- separate slice.

ALTER TABLE connector_connection RENAME TO capture_connection;
ALTER TABLE capture_connection RENAME COLUMN connector  TO provider;
ALTER TABLE capture_connection RENAME COLUMN granted_by TO user_id;

-- provider is the email/calendar/messaging capture provider (A51 email+calendar
-- parity; whatsapp/telegram connect belongs to messaging-channels but shares the
-- vocabulary).
ALTER TABLE capture_connection
  ADD CONSTRAINT capture_connection_provider_check
  CHECK (provider IN ('gmail','gcal','imap','graph','whatsapp','telegram'));

-- status: adopt the contract vocabulary and remap the improvised one in place
-- (active→connected, revoked→disconnected, error→error). Drop the old default +
-- CHECK first so the UPDATE cannot trip the constraint mid-flight.
ALTER TABLE capture_connection ALTER COLUMN status DROP DEFAULT;
ALTER TABLE capture_connection DROP CONSTRAINT IF EXISTS connector_connection_status_check;
UPDATE capture_connection SET status =
  CASE status
    WHEN 'active'  THEN 'connected'
    WHEN 'revoked' THEN 'disconnected'
    ELSE status  -- 'error' carries through unchanged
  END;
ALTER TABLE capture_connection ALTER COLUMN status SET DEFAULT 'disconnected';
ALTER TABLE capture_connection
  ADD CONSTRAINT capture_connection_status_check
  CHECK (status IN ('connected','disconnected','error','reauth_required'));

-- cursor bytea → sync_cursor jsonb. The stored bytes are already the connector's
-- JSON watermark (Gmail's {"history_id":…}); decode the bytea to text and parse.
-- A NULL cursor (never synced, or the transient path) stays NULL.
ALTER TABLE capture_connection RENAME COLUMN cursor TO sync_cursor;
ALTER TABLE capture_connection
  ALTER COLUMN sync_cursor TYPE jsonb
  USING (CASE WHEN sync_cursor IS NULL THEN NULL ELSE convert_from(sync_cursor, 'UTF8')::jsonb END);

-- The real-time-sync + base-column additions.
ALTER TABLE capture_connection ADD COLUMN watch_expires_at timestamptz NULL;  -- Gmail Pub/Sub 7-day / Graph ≤3-day renewal deadline
ALTER TABLE capture_connection ADD COLUMN archived_at      timestamptz NULL;  -- base-column convention (§1.2)

-- last_health_at / last_error were write-only diagnostics with no reader and are
-- not in CAP-DDL-2; operational health belongs to the system_log ledger.
ALTER TABLE capture_connection DROP COLUMN last_health_at;
ALTER TABLE capture_connection DROP COLUMN last_error;

-- scopes gains the ratified empty-array default.
ALTER TABLE capture_connection ALTER COLUMN scopes SET DEFAULT '{}';

-- The connection is per-user-per-provider now, not per-(connector, granting
-- human). Keep the composite (workspace_id, user_id) FK to app_user; just rename
-- it to track the column.
ALTER TABLE capture_connection DROP CONSTRAINT connector_connection_unique;
ALTER TABLE capture_connection
  ADD CONSTRAINT capture_connection_unique UNIQUE (workspace_id, user_id, provider);
ALTER TABLE capture_connection
  RENAME CONSTRAINT connector_connection_granted_by_fkey TO capture_connection_user_id_fkey;

-- Live-set index (excludes archived rows) + the watch-renewal scan.
CREATE INDEX idx_capture_connection ON capture_connection (workspace_id, provider, status)
  WHERE archived_at IS NULL;
CREATE INDEX idx_capture_watch_renew ON capture_connection (watch_expires_at)
  WHERE watch_expires_at IS NOT NULL AND status = 'connected';

-- RLS (ENABLE + FORCE) carries over the table rename untouched; rename the
-- policy for hygiene so its name tracks the table.
ALTER POLICY connector_connection_tenant_isolation ON capture_connection
  RENAME TO capture_connection_tenant_isolation;
