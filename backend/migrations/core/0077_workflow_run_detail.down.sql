-- 0077 down: drop the structured detail column. `error` was never
-- touched by the up migration, so every reason is still there.
ALTER TABLE workflow_run DROP COLUMN detail;
