-- Invited users cannot survive the narrower vocabulary; they never held a
-- session, so deactivating them loses nothing.
UPDATE app_user SET status = 'deactivated' WHERE status = 'invited';
ALTER TABLE app_user
  DROP COLUMN locked_until,
  DROP COLUMN failed_login_count;
ALTER TABLE app_user DROP CONSTRAINT app_user_status_check;
ALTER TABLE app_user ADD CONSTRAINT app_user_status_check
  CHECK (status IN ('active','suspended','deactivated'));
