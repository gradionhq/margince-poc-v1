-- Reverse of 1787050735: the lead, relationship and partner cluster carries the
-- tenant column again.
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
ALTER TABLE lead ADD COLUMN workspace_id uuid;
ALTER TABLE relationship ADD COLUMN workspace_id uuid;
ALTER TABLE partner ADD COLUMN workspace_id uuid;
ALTER TABLE dedupe_candidate ADD COLUMN workspace_id uuid;
ALTER TABLE site_read ADD COLUMN workspace_id uuid;
ALTER TABLE conversation_claim ADD COLUMN workspace_id uuid;

DO $$
DECLARE
  live uuid := (SELECT id FROM workspace WHERE archived_at IS NULL ORDER BY created_at LIMIT 1);
  t    text;
BEGIN
  FOREACH t IN ARRAY ARRAY[
    'lead', 'relationship', 'partner',
    'dedupe_candidate', 'site_read', 'conversation_claim'
  ] LOOP
    EXECUTE format('UPDATE %I SET workspace_id = $1 WHERE workspace_id IS NULL', t) USING live;
    EXECUTE format('ALTER TABLE %I ALTER COLUMN workspace_id SET NOT NULL', t);
  END LOOP;
END $$;

ALTER TABLE lead ADD CONSTRAINT lead_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE relationship ADD CONSTRAINT relationship_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE partner ADD CONSTRAINT partner_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE dedupe_candidate ADD CONSTRAINT dedupe_candidate_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE site_read ADD CONSTRAINT site_read_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE conversation_claim ADD CONSTRAINT conversation_claim_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;

DROP INDEX idx_lead_ws_live;
DROP INDEX idx_lead_owner;
DROP INDEX idx_lead_score;
DROP INDEX idx_lead_cand_org;
DROP INDEX idx_lead_linkedin;
DROP INDEX idx_lead_project;
DROP INDEX idx_rel_org_people;
DROP INDEX idx_rel_deal_stakeholders;
DROP INDEX idx_rel_partner_counterparty;
DROP INDEX idx_rel_partner_org;
DROP INDEX idx_rel_project_stakeholders;
DROP INDEX idx_partner_tier;
DROP INDEX idx_partner_stage;
DROP INDEX idx_dedupe_candidate_open;
DROP INDEX idx_site_read_org;
DROP INDEX idx_site_read_retry_due;
DROP INDEX conversation_claim_person_ix;
DROP INDEX conversation_claim_activity_ix;

CREATE INDEX idx_lead_ws_live ON lead (workspace_id, status) WHERE archived_at IS NULL;
CREATE INDEX idx_lead_owner ON lead (workspace_id, owner_id) WHERE archived_at IS NULL;
CREATE INDEX idx_lead_score ON lead (workspace_id, score DESC)
  WHERE archived_at IS NULL AND status = ANY (ARRAY['new', 'working']);
CREATE INDEX idx_lead_cand_org ON lead (workspace_id, candidate_org_key)
  WHERE candidate_org_key IS NOT NULL AND archived_at IS NULL;
CREATE INDEX idx_lead_linkedin ON lead (workspace_id, linkedin_url) WHERE linkedin_url IS NOT NULL;
CREATE INDEX idx_lead_project ON lead (workspace_id, project_id)
  WHERE project_id IS NOT NULL AND archived_at IS NULL;

CREATE INDEX idx_rel_org_people ON relationship (workspace_id, organization_id)
  WHERE kind = 'employment' AND archived_at IS NULL;
CREATE INDEX idx_rel_deal_stakeholders ON relationship (workspace_id, deal_id)
  WHERE kind = 'deal_stakeholder' AND archived_at IS NULL;
CREATE INDEX idx_rel_partner_counterparty ON relationship (workspace_id, counterparty_org_id)
  WHERE kind = ANY (ARRAY['partner_of', 'referred_by', 'co_sell_with']) AND archived_at IS NULL;
CREATE INDEX idx_rel_partner_org ON relationship (workspace_id, organization_id)
  WHERE kind = ANY (ARRAY['partner_of', 'referred_by', 'co_sell_with']) AND archived_at IS NULL;
CREATE INDEX idx_rel_project_stakeholders ON relationship (workspace_id, project_id)
  WHERE kind = 'project_stakeholder' AND archived_at IS NULL;

CREATE INDEX idx_partner_ws_live ON partner (workspace_id) WHERE archived_at IS NULL;
CREATE INDEX idx_partner_tier ON partner (workspace_id, margin_tier) WHERE archived_at IS NULL;
CREATE INDEX idx_partner_stage ON partner (workspace_id, relationship_stage) WHERE archived_at IS NULL;

CREATE INDEX idx_dedupe_candidate_open ON dedupe_candidate (workspace_id, confidence DESC)
  WHERE disposition = 'open' AND archived_at IS NULL;

CREATE INDEX idx_site_read_org ON site_read (workspace_id, organization_id, created_at DESC);
CREATE INDEX idx_site_read_retry_due ON site_read (workspace_id, next_attempt_at, id)
  WHERE status = ANY (ARRAY['deferred', 'failed']) AND next_attempt_at IS NOT NULL;

CREATE INDEX conversation_claim_person_ix ON conversation_claim (workspace_id, person_id, kind)
  WHERE archived_at IS NULL;
CREATE INDEX conversation_claim_activity_ix ON conversation_claim (workspace_id, source_activity_id)
  WHERE archived_at IS NULL;
