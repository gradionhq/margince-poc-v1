-- Reverse of 0281: the six cache tables carry the tenant column again.
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

ALTER TABLE brief_run ADD COLUMN workspace_id uuid;
ALTER TABLE brief_item ADD COLUMN workspace_id uuid;
ALTER TABLE org_brief ADD COLUMN workspace_id uuid;
ALTER TABLE org_dossier ADD COLUMN workspace_id uuid;
ALTER TABLE org_growth_fit ADD COLUMN workspace_id uuid;
ALTER TABLE person_brief ADD COLUMN workspace_id uuid;

DO $$
DECLARE ws uuid := (SELECT id FROM workspace WHERE archived_at IS NULL ORDER BY created_at LIMIT 1);
BEGIN
  UPDATE brief_run SET workspace_id = ws;
  UPDATE brief_item SET workspace_id = ws;
  UPDATE org_brief SET workspace_id = ws;
  UPDATE org_dossier SET workspace_id = ws;
  UPDATE org_growth_fit SET workspace_id = ws;
  UPDATE person_brief SET workspace_id = ws;
END $$;

ALTER TABLE brief_run ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE brief_item ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE org_brief ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE org_dossier ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE org_growth_fit ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE person_brief ALTER COLUMN workspace_id SET NOT NULL;

ALTER TABLE brief_run ADD CONSTRAINT brief_run_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE brief_item ADD CONSTRAINT brief_item_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE org_brief ADD CONSTRAINT org_brief_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE org_dossier ADD CONSTRAINT org_dossier_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE org_growth_fit ADD CONSTRAINT org_growth_fit_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE person_brief ADD CONSTRAINT person_brief_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;

ALTER TABLE brief_run ADD CONSTRAINT uq_brief_run_ws_id UNIQUE (id);

DROP INDEX idx_brief_item_deal;
CREATE INDEX idx_brief_item_deal ON brief_item (workspace_id, deal_id) WHERE state <> 'new';

DROP INDEX idx_brief_run_user;
CREATE INDEX idx_brief_run_user ON brief_run (workspace_id, user_id, generated_at DESC);

DROP INDEX org_brief_organization_ix;
CREATE INDEX org_brief_organization_ix ON org_brief (workspace_id, organization_id);

DROP INDEX org_dossier_organization_ix;
CREATE INDEX org_dossier_organization_ix ON org_dossier (workspace_id, organization_id);

DROP INDEX org_growth_fit_organization_ix;
CREATE INDEX org_growth_fit_organization_ix ON org_growth_fit (workspace_id, organization_id);

DROP INDEX person_brief_person_ix;
CREATE INDEX person_brief_person_ix ON person_brief (workspace_id, person_id);

