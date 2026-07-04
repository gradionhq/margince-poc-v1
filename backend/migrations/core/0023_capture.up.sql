-- 0023: the capture substrate (B-EP05.2/.3/.9, interfaces.md §1).
-- raw_capture keeps the re-parseable provider originals off the hot
-- path (data-model §1.6: raw is never trusted for reads, only for
-- re-derivation); connector_connection is one human's grant of one
-- connector, carrying the credential bundle and the incremental-sync
-- cursor.

CREATE TABLE raw_capture (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id  uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  source_system text NOT NULL,
  source_id     text NOT NULL,
  payload       jsonb NOT NULL,
  received_at   timestamptz NOT NULL DEFAULT now(),
  -- A replayed provider record refreshes the stored original, never
  -- duplicates it — the same natural key the domain rows dedupe on.
  CONSTRAINT raw_capture_source_unique UNIQUE (workspace_id, source_system, source_id)
);

CREATE TABLE connector_connection (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id   uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  connector      text NOT NULL,
  granted_by     uuid NOT NULL,
  -- The scope intersection frozen at grant time: descriptor scopes that
  -- the granting human actually held. Execution re-checks the human
  -- LIVE; this column records what was consented to.
  scopes         text[] NOT NULL,
  status         text NOT NULL DEFAULT 'active' CHECK (status IN ('active','revoked','error')),
  auth           bytea NULL,
  cursor         bytea NULL,
  last_health_at timestamptz NULL,
  last_error     text NULL,
  created_at     timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT connector_connection_unique UNIQUE (workspace_id, connector, granted_by),
  CONSTRAINT connector_connection_granted_by_fkey FOREIGN KEY (workspace_id, granted_by)
    REFERENCES app_user (workspace_id, id) ON DELETE CASCADE
);

-- Tenant tables ⇒ RLS, same deny-on-unset policy as every other
-- (the coverage fitness test refuses a workspace_id table without it).
ALTER TABLE raw_capture ENABLE ROW LEVEL SECURITY;
ALTER TABLE raw_capture FORCE ROW LEVEL SECURITY;
CREATE POLICY raw_capture_tenant_isolation ON raw_capture
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
ALTER TABLE connector_connection ENABLE ROW LEVEL SECURITY;
ALTER TABLE connector_connection FORCE ROW LEVEL SECURITY;
CREATE POLICY connector_connection_tenant_isolation ON connector_connection
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
