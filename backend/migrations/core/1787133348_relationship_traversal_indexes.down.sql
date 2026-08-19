-- Dropping needs ACCESS EXCLUSIVE on `relationship`, which is stronger than the
-- build's lock: the drop itself is instant, but the WAIT for it is not bounded
-- without this, and a queued ACCESS EXCLUSIVE blocks every reader and writer
-- behind it. Same three seconds, same reason as the up migration.
SET LOCAL lock_timeout = '3s';

DROP INDEX IF EXISTS idx_rel_traverse_project;
DROP INDEX IF EXISTS idx_rel_traverse_deal;
DROP INDEX IF EXISTS idx_rel_traverse_organization;
DROP INDEX IF EXISTS idx_rel_traverse_person;
