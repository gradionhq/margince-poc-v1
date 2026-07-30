-- CAP-DDL-4 (ADR-0063): the counterparty half of the running page's mirror goes.
--
-- 0141 gave every counter a page moves an inflight_* twin, so the activation
-- read could show the running page's work. For the message counts that is
-- right: a retry restates them, so they must not be committed until the page
-- commits.
--
-- The counterparty counts are not like that. A person or organization is a row
-- that EXISTS the moment it is created, and capture is idempotent — a replayed
-- message never reaches the resolver again, so no retry re-offers it to anyone.
-- Holding those counts in a mirror meant some later write had to move them, and
-- every shape of that write was wrong: fenced on the run being live, it lost
-- them to a cancel; run twice on one page, it doubled them; and being one
-- best-effort write, its failure lost the whole page's worth.
--
-- They are now counted straight into people_created / organizations_created as
-- each row is created, which is exactly-once by construction. Nothing reads
-- these two columns any more.
ALTER TABLE capture_backfill
  DROP COLUMN inflight_people,
  DROP COLUMN inflight_organizations;
