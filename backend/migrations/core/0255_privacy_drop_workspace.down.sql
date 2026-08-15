-- Reverse of 0255: the two privacy tables carry the tenant column again.
--
-- The backfill reads the LIVE workspace, and the predicate is the point: 0217's
-- pre-flight refuses to run against a database holding more than one workspace
-- with archived_at IS NULL, so there is exactly one live row — but an
-- installation that resolved to one organization by ARCHIVING the others still
-- has those rows, and 0217 names that residue explicitly. Ordering by
-- created_at alone would hand every restored row to whichever workspace
-- happened to be created first, archived or not.
--
-- If no live workspace exists and a table is not empty, SET NOT NULL fails and
-- the rollback stops — the honest outcome, since no value this migration could
-- write would be true.

ALTER TABLE retention_policy ADD COLUMN workspace_id uuid;
ALTER TABLE erasure_suppression ADD COLUMN workspace_id uuid;

DO $$
DECLARE ws uuid := (SELECT id FROM workspace WHERE archived_at IS NULL ORDER BY created_at LIMIT 1);
BEGIN
  UPDATE retention_policy SET workspace_id = ws;
  UPDATE erasure_suppression SET workspace_id = ws;
END $$;

ALTER TABLE retention_policy ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE erasure_suppression ALTER COLUMN workspace_id SET NOT NULL;

ALTER TABLE retention_policy ADD CONSTRAINT retention_policy_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE erasure_suppression ADD CONSTRAINT erasure_suppression_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
