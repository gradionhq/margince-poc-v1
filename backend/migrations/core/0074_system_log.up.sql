-- 0074: system_log — the append-only ledger of SYSTEM / non-entity
-- operational events (login, bulk export, capture skip). audit_log is the
-- P12 ledger of RECORD MUTATIONS — "who changed this record" — so its rows
-- always name an entity. A login, a filtered bulk export, or an excluded
-- personal message mutates NO record; forcing those into audit_log is what
-- made entity_id nullable there. system_log gives them their own home:
-- LEAN (no entity_type/id, no before/after images), workspace-scoped, RLS
-- ENABLE+FORCE+tenant-isolation (mirroring connector_connection, 0023),
-- append-only at the DB layer (mirroring audit_log, 0012). An entity-less
-- pipeline event (capture.skipped and its siblings, events envelope
-- pipeline class) carries this row's id as its ledger trace link
-- (Trace.AuditLogID, repurposed to "ledger row id").

CREATE TABLE system_log (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id  uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,

  actor_type    text NOT NULL CHECK (actor_type IN ('human','agent','connector','system')),
  actor_id      text NOT NULL,            -- user uuid, agent id, connector name, or 'system'
  passport_id   uuid NULL,                -- Agent Seat Passport that authorized an agent action
  on_behalf_of  uuid NULL,                -- the human authority behind an agent/connector action

  action        text NOT NULL,            -- 'login','export','capture_skip',…
  detail        jsonb NULL,               -- operation context (outcome, filter, reason, source_system,…)

  occurred_at   timestamptz NOT NULL DEFAULT now(),

  -- Same-workspace guarantee (C4, 0019 composite-FK pattern): the human
  -- authority must live in this row's workspace. Nullable on_behalf_of ⇒
  -- MATCH SIMPLE skips the check when unset; column-list SET NULL nulls only
  -- on_behalf_of on a member delete, never workspace_id.
  CONSTRAINT system_log_on_behalf_of_fkey FOREIGN KEY (workspace_id, on_behalf_of)
    REFERENCES app_user (workspace_id, id) ON DELETE SET NULL (on_behalf_of)
);
CREATE INDEX idx_system_log_actor  ON system_log (workspace_id, actor_id, occurred_at DESC);
CREATE INDEX idx_system_log_action ON system_log (workspace_id, action, occurred_at DESC);
CREATE INDEX idx_system_log_time   ON system_log (workspace_id, occurred_at DESC);

-- Append-only at the DB layer: a tamper attempt FAILS LOUDLY, never
-- silently no-ops (the audit_log_immutable() shape, 0012).
CREATE OR REPLACE FUNCTION system_log_immutable() RETURNS trigger AS $$
BEGIN
  RAISE EXCEPTION 'system_log is append-only (attempted % on row %)', TG_OP, OLD.id
    USING ERRCODE = 'check_violation';
END; $$ LANGUAGE plpgsql;
CREATE TRIGGER trg_system_log_no_mutate BEFORE UPDATE OR DELETE ON system_log
  FOR EACH ROW EXECUTE FUNCTION system_log_immutable();

ALTER TABLE system_log ENABLE ROW LEVEL SECURITY;
ALTER TABLE system_log FORCE ROW LEVEL SECURITY;
CREATE POLICY system_log_tenant_isolation ON system_log
  USING      (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
