SET LOCAL lock_timeout = '3s';

DROP TABLE IF EXISTS activity_audience_member;
ALTER TABLE activity DROP CONSTRAINT IF EXISTS activity_audience_check;
ALTER TABLE activity DROP COLUMN IF EXISTS audience;
