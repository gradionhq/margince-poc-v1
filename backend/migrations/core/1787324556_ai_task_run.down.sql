-- The DROPs below take ACCESS EXCLUSIVE on objects this migration's own up half
-- created, but the runner cannot know that, and neither can a reader of a
-- database where something else has since referenced them. Bounded for the same
-- reason every other blocking migration here is: an unbounded wait turns one
-- open transaction into a stall with no end.
SET LOCAL lock_timeout = '3s';

DROP TABLE IF EXISTS ai_task_run;
DROP FUNCTION IF EXISTS ai_task_run_state_rank(text);
DROP SEQUENCE IF EXISTS ai_task_run_seq;
