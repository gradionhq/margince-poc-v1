-- Reverse of 1787198970: the binding marker carries a pending set again.
--
-- Restored EMPTY, not repopulated. The array named workspaces a run still owed
-- a pass, and after the collapse no run tracks work that way — so there is no
-- outstanding set to reconstruct, and inventing one would tell the restored
-- fan-out to re-cover tenants whose corpus is already current. An empty set is
-- what a run holds between claim and seed, which is the state the old code
-- handles.
SET LOCAL lock_timeout = '3s';

ALTER TABLE embed_store_binding
  DROP CONSTRAINT embed_store_binding_run_shape,
  ADD COLUMN reembedding_pending uuid[] NOT NULL DEFAULT '{}',
  ADD CONSTRAINT embed_store_binding_run_shape CHECK (
    (status = 'reembedding') = (reembedding_run IS NOT NULL)
    AND (reembedding_run IS NULL) = (reembedding_identity IS NULL)
    AND (reembedding_run IS NOT NULL OR cardinality(reembedding_pending) = 0)
  );
