SET LOCAL lock_timeout = '3s';

ALTER TABLE agent_run DROP COLUMN IF EXISTS attempt;
