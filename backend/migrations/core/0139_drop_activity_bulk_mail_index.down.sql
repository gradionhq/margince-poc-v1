-- Restore the index, so reverting past 0139 leaves the schema exactly as 0137
-- defined it and 0137's own down finds the index it expects to drop.
--
-- This does rebuild the index, which is the write-blocking operation 0139
-- exists to avoid on the way FORWARD. That asymmetry is deliberate: a forward
-- migration runs unattended on every deploy, while a rollback is a deliberate
-- act an operator chooses and schedules. A down migration's job is to restore
-- the prior state, not to improve on it — leaving the index off would make
-- "reverted to 0138" mean something different from "never applied 0139".
--
-- lock_timeout bounds the ACQUISITION, not the build: if the table is busy this
-- fails fast and loud rather than queueing behind a long transaction while
-- everything else queues behind it. The operator retries in a quiet window,
-- which is when a rollback belongs anyway.
SET LOCAL lock_timeout = '3s';
CREATE INDEX IF NOT EXISTS idx_activity_bulk_mail_attested
  ON activity (workspace_id, counterparty_email)
  WHERE bulk_mail_attested AND archived_at IS NOT NULL;
