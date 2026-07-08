-- 0055: account lockout + invited status (EP03 §E: B-EP03.24 lockout per
-- formulas §27 / knobs RC-17; A97 invite-only provisioning). `invited` is
-- a user created by an admin who has not yet activated — never able to
-- log in until activation flips them to `active`. The lockout pair backs
-- the failed-login counter: the login path refuses while
-- `now() < locked_until` and resets the counter on success.
ALTER TABLE app_user DROP CONSTRAINT app_user_status_check;
ALTER TABLE app_user ADD CONSTRAINT app_user_status_check
  CHECK (status IN ('invited','active','suspended','deactivated'));
ALTER TABLE app_user
  ADD COLUMN failed_login_count integer NOT NULL DEFAULT 0 CHECK (failed_login_count >= 0),
  ADD COLUMN locked_until timestamptz NULL;
