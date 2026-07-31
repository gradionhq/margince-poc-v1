ALTER TABLE overlay_sync_state DROP COLUMN IF EXISTS mirror_frozen_at;
ALTER TABLE overlay_sync_state DROP COLUMN IF EXISTS flip_snapshot_id;
DROP TABLE IF EXISTS import_run CASCADE;
