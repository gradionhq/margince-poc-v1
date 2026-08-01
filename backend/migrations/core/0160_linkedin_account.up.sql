-- 0160: whose LinkedIn network this is (ADR-0078 §2.1b).
--
-- Onboarding asks a member to authorize LinkedIn and to name their profile.
-- Until now that answer had nowhere to live, so the question was asked and the
-- answer discarded — and the integrations tab could not show a member what it
-- believed about their own account.
--
-- One row per member per workspace. Not a column on app_user: this is people's
-- table, app_user is identity's, and a module never reaches into a sibling's.
-- The profile URL is the member's own public address, given by them about
-- themselves.

CREATE TABLE linkedin_account (
  workspace_id  uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  user_id       uuid NOT NULL,
  -- The member's public profile. Nullable because authorization and knowing
  -- the URL are separate facts: a future OAuth round trip returns the member
  -- id, and this stays whatever they typed.
  profile_url   text NULL,
  -- When consent was given. NULL means they were asked and declined, or have
  -- not been asked; the row exists either way once the act runs, so "asked
  -- and said no" is distinguishable from "never asked".
  connected_at  timestamptz NULL,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),

  PRIMARY KEY (workspace_id, user_id),
  FOREIGN KEY (workspace_id, user_id) REFERENCES app_user (workspace_id, id) ON DELETE CASCADE
);

ALTER TABLE linkedin_account ENABLE ROW LEVEL SECURITY;
ALTER TABLE linkedin_account FORCE ROW LEVEL SECURITY;
CREATE POLICY linkedin_account_tenant_isolation ON linkedin_account
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

GRANT SELECT, INSERT, UPDATE, DELETE ON linkedin_account TO margince_app;
