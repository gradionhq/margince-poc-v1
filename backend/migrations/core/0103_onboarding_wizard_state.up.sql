-- 0103: per-user onboarding orchestration survives reloads and OAuth
-- redirects without duplicating company, voice, or connector truth.
CREATE TABLE onboarding_wizard_state (
  id                 uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id       uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  user_id            uuid NOT NULL,
  path               text NOT NULL CHECK (path IN ('creator','member')),
  step               text NOT NULL CHECK (step IN ('read','confirm','voice','results','connect','complete')),
  source_mode        text NULL CHECK (source_mode IN ('website','manual')),
  website_url        text NULL,
  site_read_id       uuid NULL,
  company_draft      jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(company_draft) = 'object'),
  selected_fact_keys text[] NOT NULL DEFAULT '{}',
  voice_skipped      boolean NOT NULL DEFAULT false,
  connect_skipped    boolean NOT NULL DEFAULT false,
  version            bigint NOT NULL DEFAULT 1 CHECK (version >= 1),
  completed_at       timestamptz NULL,
  created_at         timestamptz NOT NULL DEFAULT now(),
  updated_at         timestamptz NOT NULL DEFAULT now(),
  UNIQUE (workspace_id, user_id),
  CONSTRAINT onboarding_wizard_state_user_fkey FOREIGN KEY (workspace_id, user_id)
    REFERENCES app_user (workspace_id, id) ON DELETE CASCADE,
  CONSTRAINT onboarding_wizard_state_read_fkey FOREIGN KEY (workspace_id, site_read_id)
    REFERENCES site_read (workspace_id, id) ON DELETE SET NULL (site_read_id)
);

ALTER TABLE onboarding_wizard_state ENABLE ROW LEVEL SECURITY;
ALTER TABLE onboarding_wizard_state FORCE ROW LEVEL SECURITY;
CREATE POLICY onboarding_wizard_state_tenant_isolation ON onboarding_wizard_state
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
