-- Reverse of 0232: the table takes its old name back and the three carry the
-- tenant column again.
--
-- The backfill reads the LIVE workspace, and the predicate is the point: 0217's
-- pre-flight refuses to run against a database holding more than one workspace
-- with archived_at IS NULL, so there is exactly one live row — but an
-- installation that resolved to one organization by ARCHIVING the others still
-- has those rows, and 0217 names that residue explicitly. Ordering by
-- created_at alone would hand every restored row to whichever workspace
-- happened to be created first, archived or not. If
-- `workspace` is empty and a table is not, SET NOT NULL fails and the rollback
-- stops — the honest outcome, since no value this migration could write would
-- be true.

ALTER INDEX signing_key_pkey RENAME TO workspace_signing_key_pkey;
ALTER TABLE signing_key RENAME TO workspace_signing_key;

ALTER TABLE approval ADD COLUMN workspace_id uuid;
ALTER TABLE workflow_run ADD COLUMN workspace_id uuid;
ALTER TABLE workspace_signing_key ADD COLUMN workspace_id uuid;

DO $$
DECLARE ws uuid := (SELECT id FROM workspace WHERE archived_at IS NULL ORDER BY created_at LIMIT 1);
BEGIN
  UPDATE approval SET workspace_id = ws;
  UPDATE workflow_run SET workspace_id = ws;
  UPDATE workspace_signing_key SET workspace_id = ws;
END $$;

ALTER TABLE approval ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE workflow_run ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE workspace_signing_key ALTER COLUMN workspace_id SET NOT NULL;

ALTER TABLE approval ADD CONSTRAINT approval_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE workflow_run ADD CONSTRAINT workflow_run_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE workspace_signing_key ADD CONSTRAINT workspace_signing_key_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;

ALTER TABLE approval ADD CONSTRAINT uq_approval_ws_id UNIQUE (id);

DROP INDEX idx_approval_bundle;
CREATE INDEX idx_approval_bundle ON approval (workspace_id, bundle_id, created_at) WHERE bundle_id IS NOT NULL;

DROP INDEX idx_approval_inbox;
CREATE INDEX idx_approval_inbox ON approval (workspace_id, created_at) WHERE status = 'pending';
