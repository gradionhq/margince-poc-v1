-- ADR-0072/A118 (CAP-PARAM-7): a terminal state for a read nobody wants any more.
--
-- The auto-enrich setting is re-read when the deep-read worker claims a job, so
-- an operator who switches the feature off stops paying for crawls and model
-- calls that were queued while it was on. That read did not fail — nothing went
-- wrong with it — and it did not finish, so neither existing terminal status
-- tells the truth about it.
--
-- 'cancelled' says what happened: the read was abandoned before it spent
-- anything, by a decision rather than a fault. Keeping it distinct from
-- 'failed' matters operationally, because a failure is something to investigate
-- and this is not.
--
-- The list is 0104's plus one. Rebuilding a CHECK means restating every value
-- it already carried, and 'deferred' arrived after the table was created.
ALTER TABLE site_read DROP CONSTRAINT site_read_status_check;
ALTER TABLE site_read ADD CONSTRAINT site_read_status_check
  CHECK (status IN ('queued', 'deferred', 'running', 'done', 'partial', 'failed', 'cancelled'));
