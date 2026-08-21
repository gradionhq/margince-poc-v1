SET LOCAL lock_timeout = '3s';

DROP INDEX IF EXISTS idx_attachment_extraction_activity_settled;
DROP INDEX IF EXISTS idx_attachment_extraction_activity_live;
ALTER TABLE attachment_extraction
  DROP COLUMN IF EXISTS activity_announced_at,
  DROP COLUMN IF EXISTS attempt_at,
  DROP COLUMN IF EXISTS attempt;
