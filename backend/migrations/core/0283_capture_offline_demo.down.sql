-- Narrow the vocabulary back. Any offline_demo connection is deleted first:
-- the CHECK would refuse to re-add otherwise, and a demo connection is
-- regenerable by re-running the seeder, so there is nothing here to preserve.
DELETE FROM capture_connection WHERE provider = 'offline_demo';

ALTER TABLE capture_connection
  DROP CONSTRAINT capture_connection_provider_check;

ALTER TABLE capture_connection
  ADD CONSTRAINT capture_connection_provider_check
  CHECK (provider IN ('gmail','gcal','imap','graph','whatsapp','telegram'));
