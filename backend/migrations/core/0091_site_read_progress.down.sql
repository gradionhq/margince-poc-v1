-- 0091 down: drop the live-progress hints.
ALTER TABLE site_read DROP COLUMN phase, DROP COLUMN pages_read;
