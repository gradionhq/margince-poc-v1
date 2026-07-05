-- 0043: product — the optional reusable rate-card / catalogue entry
-- (B-E03.16, data-model §12.6). Deliberately NOT CPQ (ADR-0037): no
-- bundle/option/configurator table, no pricing-rule table — a product is
-- data an offer line snapshots from, never an engine that re-prices one.
-- Money is P11: integer minor units + ISO-4217, no float column.
CREATE TABLE product (
  id               uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id     uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  name             text NOT NULL,
  sku              text NULL,
  description      text NULL,
  unit             text NOT NULL DEFAULT 'unit',           -- 'unit' | 'hour' | 'day' | … (display only, free text)
  unit_price_minor bigint NOT NULL CHECK (unit_price_minor >= 0),
  currency         char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  default_tax_rate numeric(5,2) NOT NULL DEFAULT 0 CHECK (default_tax_rate >= 0),
  active           boolean NOT NULL DEFAULT true,
  source           text NOT NULL,
  captured_by      text NOT NULL,
  version          bigint NOT NULL DEFAULT 1,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  archived_at      timestamptz NULL,
  -- target of the composite tenant FK from offer_line_item (0019 pattern)
  CONSTRAINT uq_product_ws_id UNIQUE (workspace_id, id)
);
-- SKU is optional; uniqueness binds only when one is present and live —
-- a workspace quoting fully free-form never trips it.
CREATE UNIQUE INDEX uq_product_sku ON product (workspace_id, sku)
  WHERE sku IS NOT NULL AND archived_at IS NULL;
CREATE INDEX idx_product_active ON product (workspace_id, active) WHERE archived_at IS NULL;
CREATE TRIGGER trg_product_updated BEFORE UPDATE ON product
  FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

ALTER TABLE product ENABLE ROW LEVEL SECURITY;
ALTER TABLE product FORCE ROW LEVEL SECURITY;
CREATE POLICY product_tenant_isolation ON product
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

-- Backfill the `product` RBAC object into the seeded system-role policy
-- documents of EXISTING workspaces (new workspaces get it from the
-- code-side seed). Posture: the rate-card is everyday sales material —
-- reps create and maintain entries; archiving stays manager/admin/ops
-- hygiene, mirroring the other record objects.
UPDATE role SET permissions = jsonb_set(
  permissions, '{objects,product}',
  '{"create":true,"read":true,"update":true,"delete":true}'::jsonb)
WHERE is_system AND key IN ('admin','ops','manager')
  AND NOT permissions->'objects' ? 'product';
UPDATE role SET permissions = jsonb_set(
  permissions, '{objects,product}',
  '{"create":true,"read":true,"update":true,"delete":false}'::jsonb)
WHERE is_system AND key = 'rep'
  AND NOT permissions->'objects' ? 'product';
UPDATE role SET permissions = jsonb_set(
  permissions, '{objects,product}',
  '{"create":false,"read":true,"update":false,"delete":false}'::jsonb)
WHERE is_system AND key = 'read_only'
  AND NOT permissions->'objects' ? 'product';
