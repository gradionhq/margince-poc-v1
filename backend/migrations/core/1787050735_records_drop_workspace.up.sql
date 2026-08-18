-- 1787050735: the lead, relationship and partner cluster drops the tenant column
-- (ADR-0091 §8 phase D).
--
-- Six tables, all of them about a party the installation is dealing with or
-- the edge between two of them:
--
--   lead                the unqualified prospect, before it is a contact
--   relationship        the edge: employment, deal stakeholder, partner_of
--   partner             the partner programme's own facts about an account
--   dedupe_candidate    a proposed merge of two records, awaiting a verdict
--   site_read           what the site reader fetched about an account
--   conversation_claim  a claim a message made, filed against a contact
--
-- None of the six carries a phase-B leftover constraint: their keys already
-- name something narrower than the tenant, and the only thing the column still
-- held was its foreign key to workspace.

ALTER TABLE conversation_claim DROP CONSTRAINT conversation_claim_workspace_id_fkey;
ALTER TABLE conversation_claim DROP COLUMN workspace_id;

ALTER TABLE site_read DROP CONSTRAINT site_read_workspace_id_fkey;
ALTER TABLE site_read DROP COLUMN workspace_id;

ALTER TABLE dedupe_candidate DROP CONSTRAINT dedupe_candidate_workspace_id_fkey;
ALTER TABLE dedupe_candidate DROP COLUMN workspace_id;

ALTER TABLE partner DROP CONSTRAINT partner_workspace_id_fkey;
ALTER TABLE partner DROP COLUMN workspace_id;

ALTER TABLE relationship DROP CONSTRAINT relationship_workspace_id_fkey;
ALTER TABLE relationship DROP COLUMN workspace_id;

ALTER TABLE lead DROP CONSTRAINT lead_workspace_id_fkey;
ALTER TABLE lead DROP COLUMN workspace_id;

-- The indexes that led with the column, recreated on what actually selects
-- rows. Every predicate is carried over unchanged: what these serve did not
-- change, only what they have to seek past to get there.
--
-- idx_lead_ws_live and idx_partner_ws_live are NOT recreated on the tenant —
-- the first becomes an index on status alone, the second was (workspace_id)
-- WHERE archived_at IS NULL and an index on the tenant alone has no narrowed
-- form at all.
CREATE INDEX idx_lead_ws_live ON lead (status) WHERE archived_at IS NULL;
CREATE INDEX idx_lead_owner ON lead (owner_id) WHERE archived_at IS NULL;
CREATE INDEX idx_lead_score ON lead (score DESC)
  WHERE archived_at IS NULL AND status = ANY (ARRAY['new', 'working']);
CREATE INDEX idx_lead_cand_org ON lead (candidate_org_key)
  WHERE candidate_org_key IS NOT NULL AND archived_at IS NULL;
CREATE INDEX idx_lead_linkedin ON lead (linkedin_url) WHERE linkedin_url IS NOT NULL;
CREATE INDEX idx_lead_project ON lead (project_id)
  WHERE project_id IS NOT NULL AND archived_at IS NULL;

CREATE INDEX idx_rel_org_people ON relationship (organization_id)
  WHERE kind = 'employment' AND archived_at IS NULL;
CREATE INDEX idx_rel_deal_stakeholders ON relationship (deal_id)
  WHERE kind = 'deal_stakeholder' AND archived_at IS NULL;
CREATE INDEX idx_rel_partner_counterparty ON relationship (counterparty_org_id)
  WHERE kind = ANY (ARRAY['partner_of', 'referred_by', 'co_sell_with']) AND archived_at IS NULL;
CREATE INDEX idx_rel_partner_org ON relationship (organization_id)
  WHERE kind = ANY (ARRAY['partner_of', 'referred_by', 'co_sell_with']) AND archived_at IS NULL;
CREATE INDEX idx_rel_project_stakeholders ON relationship (project_id)
  WHERE kind = 'project_stakeholder' AND archived_at IS NULL;

CREATE INDEX idx_partner_tier ON partner (margin_tier) WHERE archived_at IS NULL;
CREATE INDEX idx_partner_stage ON partner (relationship_stage) WHERE archived_at IS NULL;

CREATE INDEX idx_dedupe_candidate_open ON dedupe_candidate (confidence DESC)
  WHERE disposition = 'open' AND archived_at IS NULL;

CREATE INDEX idx_site_read_org ON site_read (organization_id, created_at DESC);
CREATE INDEX idx_site_read_retry_due ON site_read (next_attempt_at, id)
  WHERE status = ANY (ARRAY['deferred', 'failed']) AND next_attempt_at IS NOT NULL;

CREATE INDEX conversation_claim_person_ix ON conversation_claim (person_id, kind)
  WHERE archived_at IS NULL;
CREATE INDEX conversation_claim_activity_ix ON conversation_claim (source_activity_id)
  WHERE archived_at IS NULL;
