-- 0251: the recovery sweep gets an index it can actually walk.
--
-- The sweep that finds messages nothing will wake reads across the WHOLE
-- installation — it exists to find rows no workspace is watching, so it has no
-- workspace to filter on:
--
--   SELECT id, workspace_id FROM scheduled_send
--    WHERE status = 'scheduled' AND scheduled_at < $1
--    ORDER BY scheduled_at ASC LIMIT 100
--
-- 0242's idx_scheduled_send_due leads with workspace_id, which that query never
-- constrains, so Postgres cannot walk it in scheduled_at order and stop at 100.
-- Measured on 200k scheduled rows with 5k genuinely overdue: a parallel
-- sequential scan reading 4556 buffers and discarding 195k rows to find them,
-- then a top-N sort. The pass recovers nothing on almost every run and would
-- have paid that on every one, every quarter hour, forever.
--
-- Leading on scheduled_at with id and workspace_id INCLUDEd makes it an
-- index-only scan that stops at the limit: 6 buffers and 0.046ms against the
-- same data. INCLUDE rather than a composite key because neither column is
-- filtered or ordered on — they are the only two the sweep selects, and
-- carrying them in the leaf is what keeps the heap out of it.
--
-- Partial on 'scheduled' for the same reason 0242's is: the other four states
-- are terminal for the timer, and excluding them keeps this index proportional
-- to the pending backlog rather than to the send history.
--
-- 0242's index stays. It is the right one for every workspace-scoped read —
-- the rep's own list, the timer's own claim — and this one serves only the
-- installation-wide sweep.
CREATE INDEX idx_scheduled_send_recovery_due
  ON scheduled_send (scheduled_at)
  INCLUDE (id, workspace_id)
  WHERE status = 'scheduled';
