-- ACCESS EXCLUSIVE on a table this migration did not create. The change itself
-- is instant — no row is rewritten — but the lock still queues behind every
-- open transaction on the table, and an unbounded wait turns one long-running
-- reader into a total write stall. Three seconds, so a migration that cannot
-- get in fails the deploy loudly instead of holding the door.
SET LOCAL lock_timeout = '3s';

ALTER TABLE attachment_extraction DROP COLUMN IF EXISTS attempt;
