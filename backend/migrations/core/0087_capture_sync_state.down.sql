DROP TABLE IF EXISTS capture_sync_state;
ALTER TABLE capture_connection DROP CONSTRAINT IF EXISTS uq_capture_connection_ws_id;
