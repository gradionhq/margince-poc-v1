DROP INDEX IF EXISTS idx_activity_unlabeled;
DROP INDEX IF EXISTS idx_activity_thread;
ALTER TABLE activity DROP COLUMN IF EXISTS capture_labeled_at;
ALTER TABLE activity DROP COLUMN IF EXISTS capture_label;
ALTER TABLE activity DROP COLUMN IF EXISTS thread_key;
