-- Reverses 0273. Both CHECKs reference the column, so Postgres drops them with
-- it — including the one that also names password_hash, which survives.
ALTER TABLE app_user DROP COLUMN IF EXISTS must_change_password;
