DROP INDEX IF EXISTS idx_activity_meeting_host;
ALTER TABLE activity DROP CONSTRAINT IF EXISTS activity_meeting_host;
ALTER TABLE activity DROP CONSTRAINT IF EXISTS activity_host_user_id_fkey;
ALTER TABLE activity DROP COLUMN IF EXISTS host_user_id;
