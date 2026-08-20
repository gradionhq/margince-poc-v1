-- 1787198970: the re-embed run drops its per-workspace pending set
-- (ADR-0091 §8, the fan-out collapse).
--
-- reembedding_pending held the workspaces a run still had to cover, and the
-- run released its marker when the array emptied. That was a real mechanism
-- while each workspace had a corpus of its own. Phase D took the tenant column
-- off every embeddable entity, so the children the array tracked all walked the
-- SAME rows: the first rebuilt the corpus and the rest found every row already
-- fresh at the run's identity, writing nothing. The set counted jobs whose only
-- effect was to leave it.
--
-- One pass rebuilds the corpus now, and the run is either holding the marker or
-- it is not — which reembedding_run already says. Release is unconditional on
-- the run rather than fenced on an empty array.
--
-- SET LOCAL lock_timeout: this takes an ACCESS EXCLUSIVE lock on a table the
-- api writes on every reindex status read, and an unbounded wait would queue
-- behind one open transaction for as long as it lives.
SET LOCAL lock_timeout = '3s';

ALTER TABLE embed_store_binding DROP COLUMN reembedding_pending;

-- Dropping the column takes embed_store_binding_run_shape with it, and the two
-- clauses that do NOT mention the set are still the invariant: status, run and
-- identity are one fact. Re-stated here under the same name, so the marker is
-- never left un-checked and the older migration that created it still has a
-- constraint to revert.
ALTER TABLE embed_store_binding
  ADD CONSTRAINT embed_store_binding_run_shape CHECK (
    (status = 'reembedding') = (reembedding_run IS NOT NULL)
    AND (reembedding_run IS NULL) = (reembedding_identity IS NULL)
  );
