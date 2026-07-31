-- Restore the column exactly as 0100 declared it — nullable bigint, no
-- default — so reverting past 0145 leaves the schema as 0100 defined it and
-- 0100's own down migration finds the column it expects to drop. "Reverted to
-- 0146" must mean the same thing as "never applied 0147".
--
-- Restoring it costs nothing and rewrites nothing: a nullable column with no
-- default is a catalog entry, and every row reads NULL — which is what every
-- row held before the drop, since no code ever wrote this column.
--
-- lock_timeout bounds the ACQUISITION, matching the up migration: ACCESS
-- EXCLUSIVE on a table the runtime writes on every model call should fail fast
-- rather than queue while everything else queues behind it.
SET LOCAL lock_timeout = '3s';
ALTER TABLE ai_call ADD COLUMN IF NOT EXISTS estimated_cost_microusd bigint;
