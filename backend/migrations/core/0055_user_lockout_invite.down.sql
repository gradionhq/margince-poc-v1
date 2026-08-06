-- Invited users cannot survive the narrower vocabulary; they never held a
-- session, so deactivating them loses nothing.
DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);
    UPDATE app_user SET status = 'deactivated' WHERE status = 'invited';
  END LOOP;
END $$;

ALTER TABLE app_user
  DROP COLUMN locked_until,
  DROP COLUMN failed_login_count;
ALTER TABLE app_user DROP CONSTRAINT app_user_status_check;
ALTER TABLE app_user ADD CONSTRAINT app_user_status_check
  CHECK (status IN ('active','suspended','deactivated'));