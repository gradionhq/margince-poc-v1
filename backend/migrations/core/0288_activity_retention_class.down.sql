-- Dropping the columns discards the record of which activities were held under
-- a statutory obligation and until when. That is the honest outcome of undoing
-- this migration: the state has nowhere else to live, and a down that tried to
-- preserve it would be inventing a second home for it.
DROP INDEX IF EXISTS idx_activity_restricted_until;

ALTER TABLE activity
  DROP CONSTRAINT IF EXISTS activity_restriction_needs_class,
  DROP CONSTRAINT IF EXISTS activity_restriction_window,
  DROP CONSTRAINT IF EXISTS activity_restriction_complete,
  DROP CONSTRAINT IF EXISTS activity_retention_class_stamped,
  DROP CONSTRAINT IF EXISTS activity_retention_class_known;

ALTER TABLE activity
  DROP COLUMN IF EXISTS redacted_fields,
  DROP COLUMN IF EXISTS restricted_until,
  DROP COLUMN IF EXISTS restricted_reason,
  DROP COLUMN IF EXISTS restricted_at,
  DROP COLUMN IF EXISTS retention_class_at,
  DROP COLUMN IF EXISTS retention_class;
