-- 0067: quota — the per-owner/team revenue target over an explicit period
-- (RD-T06, arc 3a opener). Ported from poc-1's records-depth schema
-- (000073_records_depth_schema) with this repo's house deltas:
--
-- 1. Composite tenant FKs (0019 pattern, TestFK_tenantLocalReferencesAreComposite):
--    the assigned owner/team must live in the SAME workspace as the quota row.
--    poc-1's plain `owner_id -> app_user(id)` / `team_id -> team(id)` FKs would
--    fail that fitness function here; the 0019 target uniques
--    (uq_app_user_ws_id, uq_team_ws_id) already exist. ON DELETE mirrors the
--    house owner-ish precedent (deal_owner_id_fkey, list_team_id_fkey):
--    column-list SET NULL so only the FK column is nulled, never workspace_id.
--    A hard-delete of a quota's owner/team therefore hits the
--    quota_owner_xor_team CHECK (both sides NULL) and the whole transaction
--    is rejected — the honest outcome: reassign the quota before deleting
--    its owner, never leave it silently orphaned in neither-set state.
-- 2. House trigger (set_updated_at_bump_version, trg_quota_updated naming)
--    and RLS spelling (mirrors 0063's custom_field block) replace poc-1's
--    touch_versioned()/quota_tenant_isolation-without-FORCE shape.
-- 3. No explicit GRANT — 0015's default privileges already extend to every
--    future table the owner creates.
-- 4. Partial FK indexes on owner_id/team_id (WHERE NOT NULL, since exactly
--    one is ever set) plus a list-query index mirroring idx_deal_ws_live,
--    for the unfiltered workspace list path.

CREATE TABLE quota (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id  uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  owner_id      uuid NULL,
  team_id       uuid NULL,
  period_start  date NOT NULL,
  period_end    date NOT NULL,
  target_minor  bigint NOT NULL,
  currency      char(3) NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  version       bigint NOT NULL DEFAULT 1,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  archived_at   timestamptz NULL,

  CONSTRAINT quota_owner_xor_team CHECK ((owner_id IS NOT NULL) <> (team_id IS NOT NULL)),
  CONSTRAINT quota_period_valid   CHECK (period_end >= period_start),
  CONSTRAINT quota_owner_id_fkey FOREIGN KEY (workspace_id, owner_id)
    REFERENCES app_user (workspace_id, id) ON DELETE SET NULL (owner_id),
  CONSTRAINT quota_team_id_fkey FOREIGN KEY (workspace_id, team_id)
    REFERENCES team (workspace_id, id) ON DELETE SET NULL (team_id)
);

CREATE INDEX idx_quota_ws_live ON quota (workspace_id) WHERE archived_at IS NULL;
CREATE INDEX idx_quota_owner   ON quota (workspace_id, owner_id) WHERE owner_id IS NOT NULL;
CREATE INDEX idx_quota_team    ON quota (workspace_id, team_id) WHERE team_id IS NOT NULL;

CREATE TRIGGER trg_quota_updated BEFORE UPDATE ON quota
  FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

ALTER TABLE quota ENABLE ROW LEVEL SECURITY;
ALTER TABLE quota FORCE ROW LEVEL SECURITY;
CREATE POLICY quota_tenant_isolation ON quota
  USING      (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
