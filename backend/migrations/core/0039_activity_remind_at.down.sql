DROP INDEX idx_activity_reminders;
ALTER TABLE activity DROP CONSTRAINT activity_task_fields;
ALTER TABLE activity ADD CONSTRAINT activity_task_fields
  CHECK (kind = 'task' OR (due_at IS NULL AND assignee_id IS NULL AND is_done = false));
ALTER TABLE activity DROP COLUMN remind_at;
