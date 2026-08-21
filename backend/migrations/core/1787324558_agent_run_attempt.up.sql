-- Which attempt of one trigger occurrence the run row is currently reporting.
--
-- ACCESS EXCLUSIVE on a table this migration did not create; the change itself
-- is instant, but an unbounded wait turns one open transaction into a total
-- write stall, so the wait is bounded and a migration that cannot get in fails
-- the deploy loudly instead of holding the door.
SET LOCAL lock_timeout = '3s';

-- The AI-activity projection orders two events for one occurrence on
-- (attempt, state_rank), and a run has exactly one case where two writers
-- produce two DIFFERENT terminal states for the same attempt:
--
--   the abandoned-run sweep declares a run failed past its grace, and the
--   worker it gave up on then finishes late and corrects the row to completed.
--
-- That correction is deliberate — SaveOutcome guards on id, not on status,
-- precisely so a slow-but-alive run has the last word. Without a number that
-- rises with it, both events rank as terminal at attempt 1, the projection
-- keeps whichever arrived first, and the rail reports a run failed that the
-- source says completed. So a correction of an already-settled run begins a new
-- attempt, and the guard follows the source instead of contradicting it.
ALTER TABLE agent_run
  ADD COLUMN attempt integer NOT NULL DEFAULT 1 CHECK (attempt >= 1);
