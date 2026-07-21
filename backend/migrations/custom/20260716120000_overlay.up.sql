-- 20260716120000_overlay: the HubSpot-overlay schema cluster (fork-owned,
-- ADR-0017 custom namespace). x_sor_mode/x_incumbent flip a workspace into
-- overlay mode (design §4.2); the seven tables below are the overlay-mode
-- substrate — a read-through mirror of the incumbent SoR plus the
-- visibility/write-ledger/tombstone bookkeeping it needs (design §4.9).
ALTER TABLE workspace ADD COLUMN IF NOT EXISTS x_sor_mode text NOT NULL DEFAULT 'native'
  CHECK (x_sor_mode IN ('native','overlay'));
ALTER TABLE workspace ADD COLUMN IF NOT EXISTS x_incumbent text
  CHECK (x_incumbent IS NULL OR x_incumbent IN ('hubspot','salesforce','dynamics'));
ALTER TABLE workspace ADD CONSTRAINT x_overlay_iff_incumbent
  CHECK ((x_sor_mode = 'overlay') = (x_incumbent IS NOT NULL));

CREATE TABLE incumbent_connection (
  id uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  incumbent text NOT NULL, region text NOT NULL,
  credential_ref text NOT NULL,                 -- keyvault.Ref; never echoed
  scopes text[] NOT NULL DEFAULT '{}',
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active','revoked','error')),
  connected_at timestamptz NOT NULL DEFAULT now(), revoked_at timestamptz NULL,
  UNIQUE (workspace_id)
);
CREATE TABLE overlay_mirror (
  workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  object_class text NOT NULL, external_id text NOT NULL,
  fields jsonb NOT NULL, updated_at_baseline timestamptz NOT NULL,
  sync_state text NOT NULL DEFAULT 'fresh' CHECK (sync_state IN ('fresh','pending_sync','stale')),
  last_synced_at timestamptz NOT NULL DEFAULT now(), owner_external_id text NULL,
  PRIMARY KEY (workspace_id, object_class, external_id)
);
CREATE TABLE overlay_association (
  workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  from_type text NOT NULL, from_id text NOT NULL, to_type text NOT NULL, to_id text NOT NULL,
  type_id integer NOT NULL, category text NOT NULL, label text NULL, direction text NOT NULL,
  PRIMARY KEY (workspace_id, from_type, from_id, to_type, to_id, type_id)
);
CREATE TABLE mirror_user_map (
  workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  app_user_id uuid NOT NULL,
  incumbent text NOT NULL, incumbent_user_id text NOT NULL,
  match_source text NOT NULL DEFAULT 'email' CHECK (match_source IN ('email','manual')),
  UNIQUE (workspace_id, app_user_id, incumbent),
  -- Composite FK (workspace_id, app_user_id), not a bare app_user_id ->
  -- app_user(id): a tenant-local FK must carry workspace_id on both sides
  -- so a cross-workspace target is rejected by the database, not just by
  -- RLS row visibility (data-model tenancy integrity C4; same pattern as
  -- connector_connection's granted_by FK in 0023_capture.up.sql).
  CONSTRAINT mirror_user_map_app_user_id_fkey FOREIGN KEY (workspace_id, app_user_id)
    REFERENCES app_user (workspace_id, id) ON DELETE CASCADE
);
CREATE INDEX idx_mirror_user_map ON mirror_user_map (workspace_id, incumbent, incumbent_user_id);
CREATE TABLE mirror_visibility (
  workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  incumbent text NOT NULL, mirror_user_id uuid NOT NULL,
  object_class text NOT NULL, external_id text NOT NULL,
  can_see boolean NOT NULL, snapshot_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (workspace_id, incumbent, mirror_user_id, object_class, external_id)
);
CREATE TABLE overlay_write_ledger (                -- OVA-DDL-6; reserved, populated by branch 2
  workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  object_class text NOT NULL, external_id text NOT NULL, property text NOT NULL,
  value_hash text NOT NULL, opened_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (workspace_id, object_class, external_id, property)
);
CREATE TABLE overlay_tombstone (                   -- PII-free erasure marker (design §4.9)
  workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  object_class text NOT NULL, external_id text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (workspace_id, object_class, external_id)
);

-- Tenant tables ⇒ RLS, same deny-on-unset policy as every other (the
-- coverage fitness test refuses a workspace_id table without it) — the
-- triplet repeated verbatim for all seven overlay tables.
ALTER TABLE incumbent_connection ENABLE ROW LEVEL SECURITY;
ALTER TABLE incumbent_connection FORCE ROW LEVEL SECURITY;
CREATE POLICY incumbent_connection_tenant_isolation ON incumbent_connection
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

ALTER TABLE overlay_mirror ENABLE ROW LEVEL SECURITY;
ALTER TABLE overlay_mirror FORCE ROW LEVEL SECURITY;
CREATE POLICY overlay_mirror_tenant_isolation ON overlay_mirror
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

ALTER TABLE overlay_association ENABLE ROW LEVEL SECURITY;
ALTER TABLE overlay_association FORCE ROW LEVEL SECURITY;
CREATE POLICY overlay_association_tenant_isolation ON overlay_association
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

ALTER TABLE mirror_user_map ENABLE ROW LEVEL SECURITY;
ALTER TABLE mirror_user_map FORCE ROW LEVEL SECURITY;
CREATE POLICY mirror_user_map_tenant_isolation ON mirror_user_map
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

ALTER TABLE mirror_visibility ENABLE ROW LEVEL SECURITY;
ALTER TABLE mirror_visibility FORCE ROW LEVEL SECURITY;
CREATE POLICY mirror_visibility_tenant_isolation ON mirror_visibility
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

ALTER TABLE overlay_write_ledger ENABLE ROW LEVEL SECURITY;
ALTER TABLE overlay_write_ledger FORCE ROW LEVEL SECURITY;
CREATE POLICY overlay_write_ledger_tenant_isolation ON overlay_write_ledger
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

ALTER TABLE overlay_tombstone ENABLE ROW LEVEL SECURITY;
ALTER TABLE overlay_tombstone FORCE ROW LEVEL SECURITY;
CREATE POLICY overlay_tombstone_tenant_isolation ON overlay_tombstone
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
