-- Reverse of 0285: the quoting and delivery tables carry the tenant column again.
--
-- The backfill reads the LIVE workspace — archived_at IS NULL, oldest first.
-- 0217 refuses more than one live tenant and 0272 refuses to proceed while an
-- archived one still holds records, so there was exactly one workspace these
-- rows could have belonged to when the forward half ran. Ordering by created_at
-- alone would hand every restored row to whichever workspace was created first,
-- archived or not.
--
-- If no live workspace exists and a table is not empty, SET NOT NULL fails and
-- the rollback stops — the honest outcome, since no value this migration could
-- write would be true. A rollback on an empty database (the reverse-and-reapply
-- lane) has nothing to attribute and passes.
ALTER TABLE offer ADD COLUMN workspace_id uuid;
ALTER TABLE offer_line_item ADD COLUMN workspace_id uuid;
ALTER TABLE offer_template ADD COLUMN workspace_id uuid;
ALTER TABLE product ADD COLUMN workspace_id uuid;
ALTER TABLE project ADD COLUMN workspace_id uuid;
ALTER TABLE project_phase_history ADD COLUMN workspace_id uuid;
ALTER TABLE fx_rate ADD COLUMN workspace_id uuid;

DO $$
DECLARE
  live uuid := (SELECT id FROM workspace WHERE archived_at IS NULL ORDER BY created_at LIMIT 1);
  t    text;
BEGIN
  FOREACH t IN ARRAY ARRAY[
    'offer', 'offer_line_item', 'offer_template', 'product',
    'project', 'project_phase_history', 'fx_rate'
  ] LOOP
    EXECUTE format('UPDATE %I SET workspace_id = $1 WHERE workspace_id IS NULL', t) USING live;
    EXECUTE format('ALTER TABLE %I ALTER COLUMN workspace_id SET NOT NULL', t);
  END LOOP;
END $$;

ALTER TABLE offer ADD CONSTRAINT offer_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE offer_line_item ADD CONSTRAINT offer_line_item_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE offer_template ADD CONSTRAINT offer_template_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE product ADD CONSTRAINT product_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE project ADD CONSTRAINT project_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE project_phase_history ADD CONSTRAINT project_phase_history_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE fx_rate ADD CONSTRAINT fx_rate_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;

DROP INDEX idx_offer_deal;
DROP INDEX idx_offer_status;
DROP INDEX idx_product_active;
DROP INDEX idx_project_org;
DROP INDEX idx_project_org_open;
DROP INDEX idx_project_owner;
DROP INDEX idx_pph_project;
DROP INDEX idx_fx_rate_lookup;

CREATE INDEX idx_offer_deal ON offer (workspace_id, deal_id, revision DESC) WHERE archived_at IS NULL;
CREATE INDEX idx_offer_status ON offer (workspace_id, status) WHERE archived_at IS NULL;
CREATE INDEX idx_offer_template_ws ON offer_template (workspace_id);
CREATE INDEX idx_product_active ON product (workspace_id, active) WHERE archived_at IS NULL;
CREATE INDEX idx_project_org ON project (workspace_id, organization_id) WHERE archived_at IS NULL;
CREATE INDEX idx_project_org_open ON project (workspace_id, organization_id) WHERE phase <> 'closed' AND archived_at IS NULL;
CREATE INDEX idx_project_owner ON project (workspace_id, owner_id) WHERE archived_at IS NULL;
CREATE INDEX idx_pph_project ON project_phase_history (workspace_id, project_id, occurred_at DESC);
CREATE INDEX idx_fx_rate_lookup ON fx_rate (workspace_id, from_currency, to_currency, rate_date);

CREATE OR REPLACE FUNCTION assert_deal_project_same_org()
RETURNS trigger
LANGUAGE plpgsql
AS $fn$
BEGIN
  IF NEW.project_id IS NULL THEN
    RETURN NULL;
  END IF;
  IF NOT EXISTS (
    SELECT 1 FROM project p
    WHERE p.id = NEW.project_id
      AND p.workspace_id = NEW.workspace_id
      AND p.organization_id IS NOT DISTINCT FROM NEW.organization_id
  ) THEN
    RAISE EXCEPTION 'deal and project belong to different companies'
      USING ERRCODE = 'check_violation', CONSTRAINT = 'deal_project_same_org';
  END IF;
  RETURN NULL;
END;
$fn$;
