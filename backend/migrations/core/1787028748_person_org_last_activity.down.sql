-- Reverse: the two accelerator columns and their sort indexes go; the
-- timeline they were derived from is untouched.
DROP INDEX IF EXISTS idx_org_last_activity_keyset;
DROP INDEX IF EXISTS idx_person_last_activity_keyset;
ALTER TABLE organization DROP COLUMN IF EXISTS last_activity_at;
ALTER TABLE person       DROP COLUMN IF EXISTS last_activity_at;
