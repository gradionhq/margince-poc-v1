-- The typed relationship table (data-model §5): one table, two-plus kinds
-- with shape CHECKs. lead never appears here (ADR-0008 §2).

CREATE TABLE relationship (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id  uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  kind          text NOT NULL CHECK (kind IN ('employment','deal_stakeholder','partner_of','referred_by','co_sell_with')),

  person_id           uuid NULL REFERENCES person(id) ON DELETE CASCADE,
  organization_id     uuid NULL REFERENCES organization(id) ON DELETE CASCADE,
  counterparty_org_id uuid NULL REFERENCES organization(id) ON DELETE CASCADE,  -- the partner org, for org↔org edges
  deal_id             uuid NULL REFERENCES deal(id) ON DELETE CASCADE,

  role          text NULL,          -- employment: 'cto',… ; stakeholder: 'champion','economic_buyer','blocker','influencer','user'
  is_current_primary boolean NOT NULL DEFAULT false, -- employment: the one current primary employer
  started_at    date NULL,
  ended_at      date NULL,          -- NULL = current/ongoing

  source        text NOT NULL,
  captured_by   text NOT NULL,
  version       bigint NOT NULL DEFAULT 1,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  archived_at   timestamptz NULL,

  CONSTRAINT rel_employment_shape CHECK (
    kind <> 'employment' OR (person_id IS NOT NULL AND organization_id IS NOT NULL AND deal_id IS NULL)
  ),
  CONSTRAINT rel_stakeholder_shape CHECK (
    kind <> 'deal_stakeholder' OR (deal_id IS NOT NULL AND person_id IS NOT NULL AND organization_id IS NULL)
  ),
  CONSTRAINT rel_partner_shape CHECK (
    kind NOT IN ('partner_of','referred_by','co_sell_with')
    OR (organization_id IS NOT NULL AND counterparty_org_id IS NOT NULL
        AND organization_id <> counterparty_org_id AND person_id IS NULL AND deal_id IS NULL)
  ),
  CONSTRAINT rel_dates CHECK (ended_at IS NULL OR started_at IS NULL OR ended_at >= started_at)
);
CREATE TRIGGER trg_relationship_updated BEFORE UPDATE ON relationship
  FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

-- exactly ≤1 CURRENT-PRIMARY employer per person
CREATE UNIQUE INDEX uq_rel_current_primary_employer
  ON relationship (person_id)
  WHERE kind = 'employment' AND is_current_primary AND archived_at IS NULL;

-- "all people at org X" indexed join
CREATE INDEX idx_rel_org_people
  ON relationship (workspace_id, organization_id)
  WHERE kind = 'employment' AND archived_at IS NULL;

-- reverse: a person's orgs / employment history
CREATE INDEX idx_rel_person_orgs
  ON relationship (person_id)
  WHERE kind = 'employment' AND archived_at IS NULL;

-- deal stakeholders, both directions
CREATE INDEX idx_rel_deal_stakeholders
  ON relationship (workspace_id, deal_id)
  WHERE kind = 'deal_stakeholder' AND archived_at IS NULL;
CREATE INDEX idx_rel_stakeholder_deals
  ON relationship (person_id)
  WHERE kind = 'deal_stakeholder' AND archived_at IS NULL;

-- a stakeholder appears once per (deal, person, role)
CREATE UNIQUE INDEX uq_rel_deal_person_role
  ON relationship (deal_id, person_id, role)
  WHERE kind = 'deal_stakeholder' AND archived_at IS NULL;

-- partner edges, both directions (A41/ADR-0032)
CREATE INDEX idx_rel_partner_counterparty
  ON relationship (workspace_id, counterparty_org_id)
  WHERE kind IN ('partner_of','referred_by','co_sell_with') AND archived_at IS NULL;
CREATE INDEX idx_rel_partner_org
  ON relationship (workspace_id, organization_id)
  WHERE kind IN ('partner_of','referred_by','co_sell_with') AND archived_at IS NULL;
