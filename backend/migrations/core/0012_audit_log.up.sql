-- Audit log (data-model §11): the immutable spine of P12. Append-only at
-- the DB layer — a tamper attempt FAILS LOUDLY, never silently no-ops.

CREATE TABLE audit_log (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id  uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,

  actor_type    text NOT NULL CHECK (actor_type IN ('human','agent','system')),
  actor_id      text NOT NULL,            -- user uuid, agent id, or 'system'
  passport_id   uuid NULL,                -- Agent Seat Passport that authorized an agent action
  on_behalf_of  uuid NULL REFERENCES app_user(id) ON DELETE SET NULL,

  action        text NOT NULL CHECK (action IN ('create','update','archive','merge','promote','restore','export','erase','login','assign','advance_stage')),
  entity_type   text NOT NULL,            -- 'person','deal','lead','activity',…
  entity_id     uuid NULL,                -- NULL for non-entity actions (login/export)

  before        jsonb NULL,               -- prior state / changed fields
  after         jsonb NULL,               -- new state / changed fields
  authorization_rule text NULL,           -- which RBAC/scope rule allowed it
  evidence      jsonb NULL,               -- e.g. which inbound email triggered a promotion

  occurred_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_audit_entity ON audit_log (workspace_id, entity_type, entity_id, occurred_at DESC);
CREATE INDEX idx_audit_actor  ON audit_log (workspace_id, actor_id, occurred_at DESC);
CREATE INDEX idx_audit_time   ON audit_log (workspace_id, occurred_at DESC);

CREATE OR REPLACE FUNCTION audit_log_immutable() RETURNS trigger AS $$
BEGIN
  RAISE EXCEPTION 'audit_log is append-only (attempted % on row %)', TG_OP, OLD.id
    USING ERRCODE = 'check_violation';
END; $$ LANGUAGE plpgsql;
CREATE TRIGGER trg_audit_no_mutate BEFORE UPDATE OR DELETE ON audit_log
  FOR EACH ROW EXECUTE FUNCTION audit_log_immutable();
