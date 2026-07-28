-- IF EXISTS because 0139 drops this index in the forward direction: reversing
-- past 0139 and then past 0137 must not fail on an index that is already gone.
-- Editing a shipped migration's DOWN is safe in a way editing its UP is not —
-- no installation has run it, and the change only makes it more tolerant.
DROP INDEX IF EXISTS idx_activity_bulk_mail_attested;
ALTER TABLE activity DROP COLUMN bulk_mail_attested;
