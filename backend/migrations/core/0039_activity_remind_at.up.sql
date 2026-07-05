-- B-E16.1: the reminder column on the task model. remind_at is a
-- task-only field, enforced by widening the same per-kind CHECK that
-- guards due_at/assignee_id/is_done — one constraint owns the rule.
ALTER TABLE activity ADD COLUMN remind_at timestamptz NULL;

ALTER TABLE activity DROP CONSTRAINT activity_task_fields;
ALTER TABLE activity ADD CONSTRAINT activity_task_fields
  CHECK (kind = 'task' OR (due_at IS NULL AND assignee_id IS NULL AND is_done = false AND remind_at IS NULL));

-- The reminder scan (B-E16.6 delivery) reads "tasks whose reminder is
-- due and not done" — index exactly that slice.
CREATE INDEX idx_activity_reminders ON activity (workspace_id, remind_at)
  WHERE kind = 'task' AND remind_at IS NOT NULL AND is_done = false AND archived_at IS NULL;
