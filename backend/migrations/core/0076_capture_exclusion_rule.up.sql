-- 0074: the per-user personal-mail exclusion rule set (RC-2, capture.md
-- CAP-DDL-3). A bounded, typed (kind, value) list — sender domain,
-- recipient domain, or mail label — deliberately NOT a filtering DSL (D1).
-- The ONE capture Sink loads a user's rules and evaluates them BEFORE any
-- write, so a matching message produces zero CRM rows plus one
-- capture.skipped{personal_exclusion} event (AC1.3, EVT-SEM-10).

CREATE TABLE capture_exclusion_rule (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  -- Per connected user (RC-2 is per-user, like RC-8's connection); the
  -- composite FK mirrors connector_connection — app_user is keyed
  -- (workspace_id, id), and a vanished user takes their rules with them.
  user_id      uuid NOT NULL,
  kind         text NOT NULL CHECK (kind IN ('sender_domain','recipient_domain','label')),
  value        text NOT NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  archived_at  timestamptz NULL,
  -- Idempotent add: re-adding an existing (kind, value) for a user is a
  -- no-op, never a second row. Delete is a hard removal, so a plain unique
  -- (not partial) is correct here.
  CONSTRAINT capture_exclusion_rule_unique UNIQUE (workspace_id, user_id, kind, value),
  CONSTRAINT capture_exclusion_rule_user_fkey FOREIGN KEY (workspace_id, user_id)
    REFERENCES app_user (workspace_id, id) ON DELETE CASCADE
);

-- The per-user rule set the Sink loads before ingestion.
CREATE INDEX idx_capture_exclusion_rule ON capture_exclusion_rule (workspace_id, user_id)
  WHERE archived_at IS NULL;

-- Tenant table ⇒ RLS, same deny-on-unset policy as every other workspace
-- table (the coverage fitness test refuses a workspace_id table without it).
ALTER TABLE capture_exclusion_rule ENABLE ROW LEVEL SECURITY;
ALTER TABLE capture_exclusion_rule FORCE ROW LEVEL SECURITY;
CREATE POLICY capture_exclusion_rule_tenant_isolation ON capture_exclusion_rule
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
