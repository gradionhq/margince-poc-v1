-- Identity, tenancy & RBAC (data-model §2.1–§2.4).

CREATE TABLE workspace (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  name          text NOT NULL,
  slug          text NOT NULL,
  base_currency char(3) NOT NULL,            -- ISO-4217; IMMUTABLE after first deal (data-semantics §1.2)
  timezone      text NOT NULL DEFAULT 'UTC', -- IANA name; reporting-period zone
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  archived_at   timestamptz NULL,
  CONSTRAINT workspace_slug_unique UNIQUE (slug),
  CONSTRAINT workspace_base_currency_iso CHECK (base_currency ~ '^[A-Z]{3}$')
);
CREATE TRIGGER trg_workspace_updated BEFORE UPDATE ON workspace
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE app_user (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id  uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  email         text NOT NULL,
  password_hash text NULL,                     -- NULL for SSO-provisioned users; Argon2id, never plaintext (§2.6)
  display_name  text NOT NULL,
  timezone      text NOT NULL DEFAULT 'UTC',
  status        text NOT NULL DEFAULT 'active' CHECK (status IN ('active','suspended','deactivated')),
  is_agent      boolean NOT NULL DEFAULT false,
  -- A62/ADR-0047: 'read' = free unlimited viewer (read + read-only AI);
  -- 'full' = the billable acting seat. A hard capability ceiling below role.
  seat_type     text NOT NULL DEFAULT 'full' CHECK (seat_type IN ('read','full')),
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  archived_at   timestamptz NULL,
  CONSTRAINT app_user_agent_is_full CHECK (NOT is_agent OR seat_type = 'full')
);
-- Expression uniqueness must be an index, not a table constraint.
CREATE UNIQUE INDEX uq_app_user_email ON app_user (workspace_id, lower(email));
CREATE INDEX idx_app_user_ws ON app_user (workspace_id) WHERE archived_at IS NULL;
CREATE TRIGGER trg_app_user_updated BEFORE UPDATE ON app_user
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE team (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id   uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  name           text NOT NULL,
  parent_team_id uuid NULL REFERENCES team(id) ON DELETE SET NULL,
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),
  archived_at    timestamptz NULL,
  CONSTRAINT team_name_unique UNIQUE (workspace_id, name)
);
CREATE TRIGGER trg_team_updated BEFORE UPDATE ON team
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE team_membership (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  team_id      uuid NOT NULL REFERENCES team(id) ON DELETE CASCADE,
  user_id      uuid NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT team_membership_unique UNIQUE (team_id, user_id)
);
CREATE INDEX idx_team_membership_user ON team_membership (user_id);
CREATE INDEX idx_team_membership_team ON team_membership (team_id);
CREATE TRIGGER trg_team_membership_updated BEFORE UPDATE ON team_membership
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE role (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  key          text NOT NULL,   -- 'admin' | 'manager' | 'rep' | 'read_only' | 'ops' | <code-defined>
  name         text NOT NULL,
  is_system    boolean NOT NULL DEFAULT false,
  permissions  jsonb NOT NULL DEFAULT '{}'::jsonb, -- {object: {crud}, field_masks: [...], row_scope: own|team|all}
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  archived_at  timestamptz NULL,
  CONSTRAINT role_key_unique UNIQUE (workspace_id, key)
);
CREATE TRIGGER trg_role_updated BEFORE UPDATE ON role
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE role_assignment (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  role_id      uuid NOT NULL REFERENCES role(id) ON DELETE CASCADE,
  user_id      uuid NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
  team_id      uuid NULL REFERENCES team(id) ON DELETE CASCADE,
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now()
);
-- Uniqueness over a nullable team scope needs an expression index.
CREATE UNIQUE INDEX uq_role_assignment
  ON role_assignment (role_id, user_id, COALESCE(team_id, '00000000-0000-0000-0000-000000000000'::uuid));
CREATE INDEX idx_role_assignment_user ON role_assignment (user_id);
CREATE TRIGGER trg_role_assignment_updated BEFORE UPDATE ON role_assignment
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();
