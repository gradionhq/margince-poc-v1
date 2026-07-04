-- Organizations, domains, partner (data-model §4.1–§4.3).

CREATE TABLE organization (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id  uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  display_name  text NOT NULL,
  legal_name    text NULL,
  industry      text NULL,
  size_band     text NULL CHECK (size_band IS NULL OR size_band IN ('1-10','11-50','51-200','201-500','501-1000','1001-5000','5000+')),
  address       jsonb NULL,
  owner_id      uuid NULL REFERENCES app_user(id) ON DELETE SET NULL,

  -- company classification (A41/ADR-0032): 🟢 reversible inference with
  -- evidence, never auto-overwriting a human-set value.
  classification text NOT NULL DEFAULT 'prospect'
                  CHECK (classification IN ('prospect','customer','agency','reseller','tech_vendor','platform','partner','competitor','other')),
  relevance     smallint NULL CHECK (relevance IS NULL OR relevance BETWEEN 0 AND 100),

  -- visual identity (A55): normalized logo variants in object storage;
  -- NULL → the render layer shows a deterministic monogram.
  logo_object_key text NULL,
  logo_origin     text NULL,

  parent_org_id uuid NULL REFERENCES organization(id) ON DELETE SET NULL,
  merged_into_id uuid NULL REFERENCES organization(id) ON DELETE SET NULL,

  source        text NOT NULL,
  captured_by   text NOT NULL,
  raw           jsonb NULL,

  search_tsv    tsvector GENERATED ALWAYS AS (
                  to_tsvector('simple',
                    coalesce(display_name,'') || ' ' || coalesce(legal_name,'') || ' ' || coalesce(industry,''))
                ) STORED,

  version       bigint NOT NULL DEFAULT 1,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  archived_at   timestamptz NULL,

  CONSTRAINT organization_not_own_parent CHECK (parent_org_id IS NULL OR parent_org_id <> id)
);
CREATE INDEX idx_org_ws_live ON organization (workspace_id) WHERE archived_at IS NULL;
CREATE INDEX idx_org_owner   ON organization (workspace_id, owner_id) WHERE archived_at IS NULL;
CREATE INDEX idx_org_parent  ON organization (parent_org_id) WHERE parent_org_id IS NOT NULL;
CREATE INDEX idx_org_class   ON organization (workspace_id, classification) WHERE archived_at IS NULL;
CREATE INDEX idx_org_search  ON organization USING gin (search_tsv);
CREATE TRIGGER trg_organization_updated BEFORE UPDATE ON organization
  FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

-- Transitive acyclicity for the org hierarchy (data-model §4.1 note): a
-- plain CHECK covers only the self-parent case; this walk blocks any cycle.
CREATE OR REPLACE FUNCTION organization_no_ancestor_cycle() RETURNS trigger AS $$
DECLARE
  ancestor uuid := NEW.parent_org_id;
BEGIN
  WHILE ancestor IS NOT NULL LOOP
    IF ancestor = NEW.id THEN
      RAISE EXCEPTION 'organization % would become its own ancestor', NEW.id
        USING ERRCODE = 'check_violation';
    END IF;
    SELECT parent_org_id INTO ancestor FROM organization WHERE id = ancestor;
  END LOOP;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE TRIGGER trg_organization_no_cycle BEFORE INSERT OR UPDATE OF parent_org_id ON organization
  FOR EACH ROW WHEN (NEW.parent_org_id IS NOT NULL)
  EXECUTE FUNCTION organization_no_ancestor_cycle();

CREATE TABLE organization_domain (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id    uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  organization_id uuid NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
  domain          text NOT NULL,        -- lowercased, no scheme, no www
  is_primary      boolean NOT NULL DEFAULT false,
  source          text NOT NULL,
  captured_by     text NOT NULL,
  version         bigint NOT NULL DEFAULT 1,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  archived_at     timestamptz NULL,
  CONSTRAINT org_domain_norm CHECK (domain = lower(domain))
);
CREATE UNIQUE INDEX uq_org_domain ON organization_domain (workspace_id, domain) WHERE archived_at IS NULL;
CREATE UNIQUE INDEX uq_org_domain_primary ON organization_domain (organization_id) WHERE is_primary AND archived_at IS NULL;
CREATE INDEX idx_org_domain_org ON organization_domain (organization_id) WHERE archived_at IS NULL;
CREATE TRIGGER trg_organization_domain_updated BEFORE UPDATE ON organization_domain
  FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

-- Partner: program state (A38) + relationship lifecycle (A68/ADR-0053);
-- 1:1 extension of organization. Behavior for the lifecycle columns is
-- Fast-follow; the schema is V1-forward-compatible.
CREATE TABLE partner (
  id                 uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id       uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  organization_id    uuid NOT NULL UNIQUE REFERENCES organization(id) ON DELETE CASCADE,

  cert_status        text NOT NULL DEFAULT 'applied'
                       CHECK (cert_status IN ('applied','certified','suspended')),
  partner_role       text NULL CHECK (partner_role IS NULL OR partner_role IN ('hosting','consulting','strategic')),
  margin_tier        text NULL CHECK (margin_tier IS NULL OR margin_tier IN ('tier1_15','tier2_20','tier3_25')),
  certified_staff    smallint NOT NULL DEFAULT 0,
  retention_rate     smallint NULL CHECK (retention_rate IS NULL OR retention_rate BETWEEN 0 AND 100),
  joined_at          date NULL,
  renews_at          date NULL,

  relationship_stage  text NOT NULL DEFAULT 'research'
                        CHECK (relationship_stage IN ('research','identified','contacted','in_conversation',
                                                      'fit_confirmed','agreement_pending','active','active_referring',
                                                      'dormant','no_fit')),
  partner_fit_score   smallint NULL CHECK (partner_fit_score IS NULL OR partner_fit_score BETWEEN 0 AND 100),
  relationship_health numeric(3,2) NULL CHECK (relationship_health IS NULL OR relationship_health BETWEEN 0 AND 1),
  last_contact_at     timestamptz NULL,
  next_step           text NULL,
  next_step_due_at    date NULL,
  served_segments     text[] NULL,

  source             text NOT NULL,
  captured_by        text NOT NULL,
  raw                jsonb NULL,
  version            bigint NOT NULL DEFAULT 1,
  created_at         timestamptz NOT NULL DEFAULT now(),
  updated_at         timestamptz NOT NULL DEFAULT now(),
  archived_at        timestamptz NULL
);
CREATE INDEX idx_partner_ws_live ON partner (workspace_id) WHERE archived_at IS NULL;
CREATE INDEX idx_partner_tier    ON partner (workspace_id, margin_tier) WHERE archived_at IS NULL;
CREATE INDEX idx_partner_stage   ON partner (workspace_id, relationship_stage) WHERE archived_at IS NULL;
CREATE TRIGGER trg_partner_updated BEFORE UPDATE ON partner
  FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();
