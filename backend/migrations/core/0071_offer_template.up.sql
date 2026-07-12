-- 0071: offer_template — the branded, governed DE/EN PDF layout an offer
-- renders against (OP-T02/OP-T03, OFFER-DDL-4). Ported from poc-1's
-- records-depth schema with this repo's house deltas:
--
-- 1. Composite tenant FK convention (0019 pattern,
--    TestFK_tenantLocalReferencesAreComposite): offer_template carries no
--    tenant-local FK of its own (only the root workspace(id) reference),
--    but it IS a composite-FK TARGET — offer.template_id below needs a
--    (workspace_id, id) unique key on this side, mirroring offer's own
--    uq_offer_ws_id (0044).
-- 2. House trigger (set_updated_at_bump_version, trg_offer_template_updated
--    naming) and RLS spelling mirror 0044/0063's shape, replacing poc-1's
--    touch_versioned()/RLS-without-FORCE.
-- 3. No explicit GRANT — 0015's default privileges already extend to every
--    future table the owner creates.
-- 4. No source/captured_by columns: unlike captured CRM data, a template
--    is workspace-authored config (mirrors poc-1's DDL-4 pin).

CREATE TABLE offer_template (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id  uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  name          text NOT NULL,
  locale        text NOT NULL DEFAULT 'de-DE',
  is_default    boolean NOT NULL DEFAULT false,
  layout        jsonb NOT NULL,
  version       bigint NOT NULL DEFAULT 1,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  archived_at   timestamptz NULL,

  CONSTRAINT offer_template_name_unique UNIQUE (workspace_id, name),
  -- target of the composite tenant FK from offer.template_id (0019 pattern)
  CONSTRAINT uq_offer_template_ws_id UNIQUE (workspace_id, id)
);

-- At most one default template per locale, live templates only — an
-- archived former default never blocks picking a new one.
CREATE UNIQUE INDEX uq_offer_template_default ON offer_template (workspace_id, locale)
  WHERE is_default AND archived_at IS NULL;
CREATE INDEX idx_offer_template_ws ON offer_template (workspace_id);

CREATE TRIGGER trg_offer_template_updated BEFORE UPDATE ON offer_template
  FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

ALTER TABLE offer_template ENABLE ROW LEVEL SECURITY;
ALTER TABLE offer_template FORCE ROW LEVEL SECURITY;
CREATE POLICY offer_template_tenant_isolation ON offer_template
  USING      (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

-- offer.template_id — the offer's chosen render template (unset falls back
-- to the workspace's default template for the offer's locale, WP7/OP-T02).
-- Column-list SET NULL (the quota/deal owner-ish precedent, 0067): deleting
-- a template detaches it from offers that used it without touching any
-- other column, and never re-derives workspace_id from the FK target.
ALTER TABLE offer ADD COLUMN template_id uuid NULL;
ALTER TABLE offer ADD CONSTRAINT offer_template_id_fkey FOREIGN KEY (workspace_id, template_id)
  REFERENCES offer_template (workspace_id, id) ON DELETE SET NULL (template_id);
CREATE INDEX idx_offer_template_fk ON offer (template_id) WHERE template_id IS NOT NULL;
