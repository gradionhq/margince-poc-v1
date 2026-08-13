-- 0232: the approval spine drops the tenant column, and the signing-key table
-- drops it from its NAME (ADR-0091 §8 phase D, §1).
--
-- Three tables. `automation` is deliberately not among them: migrations 0148
-- and 0149 are a one-off reminder repair whose SQL reads
-- automation.workspace_id, and two integration suites replay that SQL against
-- the head schema to prove the repair did what it claimed. Dropping the column
-- here would retire eleven tests as a side effect of a column drop. It goes
-- with `activity`, whose column those same migrations also read.
--
-- `workspace_signing_key` becomes `signing_key`: ADR-0091 §1
-- names it as one of the two tables whose name carries the boundary, and a
-- table called workspace_* with no workspace column would be the worst of both
-- — the reader still has to ask which workspace, and the schema no longer
-- answers. Its primary key is `kid`, so nothing else moves with the rename.
--
-- `idx_approval_target` is untouched on purpose: it already leads with
-- target_entity_id, which is the lookup, and the tenant never prefixed it.

DROP INDEX idx_approval_bundle;
CREATE INDEX idx_approval_bundle ON approval (bundle_id, created_at) WHERE bundle_id IS NOT NULL;

DROP INDEX idx_approval_inbox;
CREATE INDEX idx_approval_inbox ON approval (created_at) WHERE status = 'pending';

ALTER TABLE approval DROP CONSTRAINT uq_approval_ws_id;

ALTER TABLE approval DROP COLUMN workspace_id;
ALTER TABLE workflow_run DROP COLUMN workspace_id;
ALTER TABLE workspace_signing_key DROP COLUMN workspace_id;

ALTER TABLE workspace_signing_key RENAME TO signing_key;
ALTER INDEX workspace_signing_key_pkey RENAME TO signing_key_pkey;
