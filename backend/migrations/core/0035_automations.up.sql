-- 0035: automation instances (data-model §12.5, un-deferred by
-- feedback/14 for the EP09.15 editor). One row per configured instance
-- of a closed-catalog type; trigger/action are snapshots of the catalog
-- entry at create time (the registry in code stays the source of truth
-- — B-E15.1), params are validated against the entry's schema in code.
-- The wire `status enabled|paused` maps to the DDL-faithful `enabled`
-- boolean; deletion is a soft archive (the audit vocabulary has no
-- `delete` verb, and a vanished instance would orphan its run records).
CREATE TABLE automation (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id  uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  key           text NOT NULL,
  name          text NOT NULL,
  origin        text NOT NULL DEFAULT 'catalog' CHECK (origin IN ('catalog','agent_authored')),
  trigger       jsonb NOT NULL,
  action        jsonb NOT NULL,
  params        jsonb NOT NULL DEFAULT '{}'::jsonb,
  owner_id      uuid NULL,
  enabled       boolean NOT NULL DEFAULT false,   -- created paused (contract); the bootstrap seed enables the starters explicitly
  tier          text NOT NULL DEFAULT 'green' CHECK (tier IN ('green','yellow')),
  version       bigint NOT NULL DEFAULT 1,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NULL,
  archived_at   timestamptz NULL,
  CONSTRAINT uq_automation_ws_id UNIQUE (workspace_id, id),
  CONSTRAINT automation_owner_fkey FOREIGN KEY (workspace_id, owner_id)
    REFERENCES app_user (workspace_id, id) ON DELETE SET NULL (owner_id)
);
CREATE INDEX idx_automation_ws_key_live ON automation (workspace_id, key)
  WHERE enabled AND archived_at IS NULL;

ALTER TABLE automation ENABLE ROW LEVEL SECURITY;
ALTER TABLE automation FORCE ROW LEVEL SECURITY;
CREATE POLICY automation_tenant_isolation ON automation
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

-- Backfill the `automation` RBAC object into the seeded system-role
-- policy documents of EXISTING workspaces (new workspaces get it from
-- the code-side seed). Config posture mirrors `pipeline`
-- (decisions/0006): admin/ops configure, everyone else reads.
UPDATE role SET permissions = jsonb_set(
  permissions, '{objects,automation}',
  '{"create":true,"read":true,"update":true,"delete":true}'::jsonb)
WHERE is_system AND key IN ('admin','ops')
  AND NOT permissions->'objects' ? 'automation';
UPDATE role SET permissions = jsonb_set(
  permissions, '{objects,automation}',
  '{"create":false,"read":true,"update":false,"delete":false}'::jsonb)
WHERE is_system AND key IN ('manager','rep','read_only')
  AND NOT permissions->'objects' ? 'automation';
