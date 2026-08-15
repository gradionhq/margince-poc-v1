-- 0255: the privacy tables drop the tenant column (ADR-0091 §8 phase D).
--
-- Held back until now because the retention sweep fanned out one job per
-- workspace and its suite proved that one tenant's failure cost that tenant its
-- pass and nothing more. The pass is one job now (ADR-0103 §1).
--
-- No index narrows here: neither table has one that leads with the tenant. The
-- keys already read (object_type, category) and (kind, value_hash), which is
-- what phase B left behind — and erasure_suppression's primary key is the
-- reason its own column was never in a lookup, since a suppression is keyed on
-- what it suppresses.

ALTER TABLE retention_policy DROP COLUMN workspace_id;
ALTER TABLE erasure_suppression DROP COLUMN workspace_id;
