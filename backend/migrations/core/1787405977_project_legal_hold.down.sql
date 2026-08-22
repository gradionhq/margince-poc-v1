-- Reverses 1787405977: a project can no longer be placed under legal hold.
SET LOCAL lock_timeout = '3s';

ALTER TABLE project DROP COLUMN legal_hold;
