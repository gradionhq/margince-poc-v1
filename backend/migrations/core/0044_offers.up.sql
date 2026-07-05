-- 0044: offer + offer_line_item — the versioned, deal-bound Angebot with
-- typed line items (B-E03.17, data-model §12.6; A48/ADR-0037). Totals
-- (net/tax/gross) are DERIVED from the lines in code and stored only so
-- the sent document is a fixed record; line-level totals are never
-- stored at all. Money is P11: integer minor units + ISO-4217.
CREATE TABLE offer (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id    uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  deal_id         uuid NOT NULL,
  offer_number    text NOT NULL,                            -- human-facing "Angebot" number, unique per workspace (with revision)
  revision        integer NOT NULL DEFAULT 1 CHECK (revision >= 1),
  status          text NOT NULL DEFAULT 'draft'
                    CHECK (status IN ('draft','sent','accepted','rejected','expired','superseded')),
  currency        char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),

  -- snapshots: a sent offer is a fixed record even if the deal/org later change
  buyer_org_id    uuid NULL,
  buyer_snapshot  jsonb NULL,                               -- buyer legal block captured at send time
  issuer_snapshot jsonb NULL,                               -- seller legal block captured at send time

  valid_until     date NULL,                                -- offer expiry (drives 'expired')
  intro_text      text NULL,                                -- drafted / templated cover blurb
  terms_text      text NULL,                                -- boilerplate terms block

  -- money totals, DERIVED from line items in code; stored for the record
  net_minor       bigint NOT NULL DEFAULT 0,
  tax_minor       bigint NOT NULL DEFAULT 0,
  gross_minor     bigint NOT NULL DEFAULT 0,
  fx_rate_to_base numeric(20,10) NULL,                      -- frozen at send (base-currency roll-up, RT-PR-C2)
  fx_rate_date    date NULL,

  pdf_asset_ref   text NULL,                                -- rendered PDF ref (render itself is the WP7 slice)
  accepted_at     timestamptz NULL,
  source          text NOT NULL,
  captured_by     text NOT NULL,
  version         bigint NOT NULL DEFAULT 1,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  archived_at     timestamptz NULL,

  CONSTRAINT offer_number_rev_unique UNIQUE (workspace_id, offer_number, revision),
  CONSTRAINT offer_accepted_at CHECK (status <> 'accepted' OR accepted_at IS NOT NULL),
  -- target of the composite tenant FK from offer_line_item (0019 pattern)
  CONSTRAINT uq_offer_ws_id UNIQUE (workspace_id, id),
  -- tenant-pinned deal reference: the FK can never straddle workspaces
  CONSTRAINT offer_deal_fkey FOREIGN KEY (workspace_id, deal_id)
    REFERENCES deal (workspace_id, id) ON DELETE RESTRICT,
  CONSTRAINT offer_buyer_org_fkey FOREIGN KEY (workspace_id, buyer_org_id)
    REFERENCES organization (workspace_id, id) ON DELETE SET NULL (buyer_org_id)
);
CREATE INDEX idx_offer_deal   ON offer (workspace_id, deal_id, revision DESC) WHERE archived_at IS NULL;
CREATE INDEX idx_offer_status ON offer (workspace_id, status) WHERE archived_at IS NULL;
CREATE TRIGGER trg_offer_updated BEFORE UPDATE ON offer
  FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

ALTER TABLE offer ENABLE ROW LEVEL SECURITY;
ALTER TABLE offer FORCE ROW LEVEL SECURITY;
CREATE POLICY offer_tenant_isolation ON offer
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

-- A typed line: price/description are SNAPSHOTS copied from the optional
-- product at line-creation time — a rate-card change never re-prices an
-- existing line. Line net/tax/total are derived in code, never stored:
-- a stored free value could drift from the lines and there is exactly
-- one totals spelling (B-E03.18).
CREATE TABLE offer_line_item (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id     uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  offer_id         uuid NOT NULL,
  position         integer NOT NULL CHECK (position >= 1), -- display order, unique per offer
  product_id       uuid NULL,                              -- optional rate-card ref; the line survives the product
  description      text NOT NULL,
  unit             text NOT NULL DEFAULT 'unit',
  quantity         numeric(14,3) NOT NULL CHECK (quantity > 0),
  unit_price_minor bigint NOT NULL CHECK (unit_price_minor >= 0),
  discount_pct     numeric(5,2) NOT NULL DEFAULT 0 CHECK (discount_pct BETWEEN 0 AND 100),
  tax_rate         numeric(5,2) NOT NULL DEFAULT 0 CHECK (tax_rate >= 0),
  evidence         jsonb NULL,                             -- {snippet, source_id} when AI-drafted (evidence-or-omit)
  version          bigint NOT NULL DEFAULT 1,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT uq_oli_position UNIQUE (offer_id, position),
  CONSTRAINT oli_offer_fkey FOREIGN KEY (workspace_id, offer_id)
    REFERENCES offer (workspace_id, id) ON DELETE CASCADE,
  CONSTRAINT oli_product_fkey FOREIGN KEY (workspace_id, product_id)
    REFERENCES product (workspace_id, id) ON DELETE SET NULL (product_id)
);
CREATE INDEX idx_oli_offer ON offer_line_item (offer_id, position);
CREATE TRIGGER trg_oli_updated BEFORE UPDATE ON offer_line_item
  FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

ALTER TABLE offer_line_item ENABLE ROW LEVEL SECURITY;
ALTER TABLE offer_line_item FORCE ROW LEVEL SECURITY;
CREATE POLICY offer_line_item_tenant_isolation ON offer_line_item
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

-- Backfill the `offer` RBAC object into the seeded system-role policy
-- documents of EXISTING workspaces (new workspaces get it from the
-- code-side seed). Posture mirrors deal: reps create and work offers but
-- never delete them; managers/admin/ops carry delete.
UPDATE role SET permissions = jsonb_set(
  permissions, '{objects,offer}',
  '{"create":true,"read":true,"update":true,"delete":true}'::jsonb)
WHERE is_system AND key IN ('admin','ops','manager')
  AND NOT permissions->'objects' ? 'offer';
UPDATE role SET permissions = jsonb_set(
  permissions, '{objects,offer}',
  '{"create":true,"read":true,"update":true,"delete":false}'::jsonb)
WHERE is_system AND key = 'rep'
  AND NOT permissions->'objects' ? 'offer';
UPDATE role SET permissions = jsonb_set(
  permissions, '{objects,offer}',
  '{"create":false,"read":true,"update":false,"delete":false}'::jsonb)
WHERE is_system AND key = 'read_only'
  AND NOT permissions->'objects' ? 'offer';
