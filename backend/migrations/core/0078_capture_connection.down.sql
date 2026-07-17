-- Reverse 0078: capture_connection → connector_connection, mirror order.

ALTER POLICY capture_connection_tenant_isolation ON capture_connection
  RENAME TO connector_connection_tenant_isolation;

DROP INDEX idx_capture_watch_renew;
DROP INDEX idx_capture_connection;

ALTER TABLE capture_connection
  RENAME CONSTRAINT capture_connection_user_id_fkey TO connector_connection_granted_by_fkey;
ALTER TABLE capture_connection DROP CONSTRAINT capture_connection_unique;
-- Restore the original per-(connector, granting-human) unique key (columns still
-- carry their up-side names here; the RENAME COLUMNs below track them).
ALTER TABLE capture_connection
  ADD CONSTRAINT connector_connection_unique UNIQUE (workspace_id, user_id, provider);

ALTER TABLE capture_connection ALTER COLUMN scopes DROP DEFAULT;

-- Restore the write-only diagnostics dropped by the up.
ALTER TABLE capture_connection ADD COLUMN last_error     text        NULL;
ALTER TABLE capture_connection ADD COLUMN last_health_at timestamptz NULL;

ALTER TABLE capture_connection DROP COLUMN archived_at;
ALTER TABLE capture_connection DROP COLUMN watch_expires_at;

-- sync_cursor jsonb → cursor bytea (serialize the JSON text back to bytes).
ALTER TABLE capture_connection
  ALTER COLUMN sync_cursor TYPE bytea
  USING (CASE WHEN sync_cursor IS NULL THEN NULL ELSE convert_to(sync_cursor::text, 'UTF8') END);
ALTER TABLE capture_connection RENAME COLUMN sync_cursor TO cursor;

-- status: back to the improvised vocabulary (connected→active,
-- disconnected→revoked, error/reauth_required→error).
ALTER TABLE capture_connection ALTER COLUMN status DROP DEFAULT;
ALTER TABLE capture_connection DROP CONSTRAINT capture_connection_status_check;
UPDATE capture_connection SET status =
  CASE status
    WHEN 'connected'    THEN 'active'
    WHEN 'disconnected' THEN 'revoked'
    ELSE 'error'  -- 'error' and the never-in-old-schema 'reauth_required' both land on 'error'
  END;
ALTER TABLE capture_connection ALTER COLUMN status SET DEFAULT 'active';
ALTER TABLE capture_connection
  ADD CONSTRAINT connector_connection_status_check CHECK (status IN ('active','revoked','error'));

ALTER TABLE capture_connection DROP CONSTRAINT capture_connection_provider_check;

ALTER TABLE capture_connection RENAME COLUMN user_id  TO granted_by;
ALTER TABLE capture_connection RENAME COLUMN provider TO connector;
ALTER TABLE capture_connection RENAME TO connector_connection;
