-- 0285: the quoting and delivery tables drop the tenant column (ADR-0091 §8 phase D).
--
-- Seven of the deals module's twelve, taken together because they are the half
-- that hangs off a deal rather than being one:
--
--   what we quoted     offer, offer_line_item, offer_template
--   what we sell       product
--   what we deliver    project, project_phase_history
--   what it is worth   fx_rate
--
-- The deal spine itself (deal, pipeline, stage and the two histories) follows in
-- its own change: deal is the most-referenced table in the schema after person,
-- and mixing the two would make a diff nobody can review.
--
-- Every key here already names something narrower than the tenant — an offer's
-- (offer_number, revision), a product's sku, a rate's (from, to, date) — so
-- phase B left nothing composite to collapse.

ALTER TABLE offer_line_item DROP CONSTRAINT offer_line_item_workspace_id_fkey;
ALTER TABLE offer_line_item DROP COLUMN workspace_id;

ALTER TABLE offer DROP CONSTRAINT offer_workspace_id_fkey;
ALTER TABLE offer DROP COLUMN workspace_id;

ALTER TABLE offer_template DROP CONSTRAINT offer_template_workspace_id_fkey;
ALTER TABLE offer_template DROP COLUMN workspace_id;

ALTER TABLE product DROP CONSTRAINT product_workspace_id_fkey;
ALTER TABLE product DROP COLUMN workspace_id;

ALTER TABLE project_phase_history DROP CONSTRAINT project_phase_history_workspace_id_fkey;
ALTER TABLE project_phase_history DROP COLUMN workspace_id;

ALTER TABLE project DROP CONSTRAINT project_workspace_id_fkey;
ALTER TABLE project DROP COLUMN workspace_id;

ALTER TABLE fx_rate DROP CONSTRAINT fx_rate_workspace_id_fkey;
ALTER TABLE fx_rate DROP COLUMN workspace_id;

-- The indexes that led with the column, recreated on what actually selects rows.
--
-- idx_offer_template_ws is NOT recreated, because there is nothing left of it:
-- it was (workspace_id) alone, and an index on the tenant by itself has no
-- narrowed form — the same reading idx_quota_ws_live got.
CREATE INDEX idx_offer_deal ON offer (deal_id, revision DESC) WHERE archived_at IS NULL;
CREATE INDEX idx_offer_status ON offer (status) WHERE archived_at IS NULL;
CREATE INDEX idx_product_active ON product (active) WHERE archived_at IS NULL;
CREATE INDEX idx_project_org ON project (organization_id) WHERE archived_at IS NULL;
CREATE INDEX idx_project_org_open ON project (organization_id) WHERE phase <> 'closed' AND archived_at IS NULL;
CREATE INDEX idx_project_owner ON project (owner_id) WHERE archived_at IS NULL;
CREATE INDEX idx_pph_project ON project_phase_history (project_id, occurred_at DESC);
CREATE INDEX idx_fx_rate_lookup ON fx_rate (from_currency, to_currency, rate_date);

-- The deal↔project same-company trigger reads `project` by id and workspace.
-- Its own comment says the workspace leg changes no outcome — the composite FK
-- already forbade a cross-workspace project_id — and it was kept so the rule
-- could be judged without reading the RLS policy beside it. There is no tenant
-- column left to read, and the rule it enforces was never about tenancy: a deal
-- and its project must name the same company.
--
-- CREATE OR REPLACE rather than DROP and recreate: the trigger keeps pointing at
-- the same function, so nothing has to be rebound.
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
      AND p.organization_id IS NOT DISTINCT FROM NEW.organization_id
  ) THEN
    RAISE EXCEPTION 'deal and project belong to different companies'
      USING ERRCODE = 'check_violation', CONSTRAINT = 'deal_project_same_org';
  END IF;
  RETURN NULL;
END;
$fn$;
