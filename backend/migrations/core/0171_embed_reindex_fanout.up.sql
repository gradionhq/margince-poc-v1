-- The fleet-wide re-embed fans out to one job row per workspace, so the marker
-- has to hold the RUN and not just the fact that one exists: which run owns it,
-- which identity that run targets, and which workspaces it is still waiting on.
-- A run owns the marker exactly while that set is non-empty; the last workspace
-- out releases it.

-- A run claimed under the single-row shape has no pending set and no job that
-- can still fan out, so the new shape's invariant is stated by giving the marker
-- back rather than by carrying a claim nothing will ever finish.
UPDATE embed_store_binding SET status = 'idle', updated_at = now() WHERE status = 'reembedding';

ALTER TABLE embed_store_binding
  -- The run id is the fence, NOT the target identity: a forced rebuild re-runs
  -- deliberately under the SAME identity, so an identity fence would let a
  -- straggler of a finished run act on the marker of the run that replaced it.
  ADD COLUMN reembedding_run      uuid,
  ADD COLUMN reembedding_identity text,                         -- what the run targets; stamped on release
  ADD COLUMN reembedding_pending  uuid[] NOT NULL DEFAULT '{}',  -- workspaces the run has not finished
  ADD CONSTRAINT embed_store_binding_run_shape CHECK (
    -- The status, the run, and the identity it targets are ONE fact, so they
    -- cannot drift apart, and an unclaimed marker carries no leftover set.
    (status = 'reembedding') = (reembedding_run IS NOT NULL)
    AND (reembedding_run IS NULL) = (reembedding_identity IS NULL)
    AND (reembedding_run IS NOT NULL OR cardinality(reembedding_pending) = 0)
  );
