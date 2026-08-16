DROP INDEX IF EXISTS idx_person_email_correspondence;
ALTER TABLE person_email DROP COLUMN IF EXISTS from_correspondence;
