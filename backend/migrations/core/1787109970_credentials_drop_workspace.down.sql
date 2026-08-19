-- Reverse of 1787109970: the credential tables carry the tenant column again.
--
-- The backfill reads the LIVE workspace — archived_at IS NULL, oldest first.
-- 0217 refuses more than one live tenant and 0272 refuses to proceed while an
-- archived one still holds records, so there was exactly one workspace these
-- rows could have belonged to when the forward half ran. Ordering by created_at
-- alone would hand every restored row to whichever workspace was created first,
-- archived or not.
--
-- If no live workspace exists and a table is not empty, SET NOT NULL fails and
-- the rollback stops — the honest outcome, since no value this migration could
-- write would be true. A rollback on an empty database (the reverse-and-reapply
-- lane) has nothing to attribute and passes.
ALTER TABLE passport ADD COLUMN workspace_id uuid;
ALTER TABLE oauth_client ADD COLUMN workspace_id uuid;
ALTER TABLE oauth_grant ADD COLUMN workspace_id uuid;
ALTER TABLE oauth_authorization_code ADD COLUMN workspace_id uuid;
ALTER TABLE oauth_refresh_token ADD COLUMN workspace_id uuid;
ALTER TABLE auth_token ADD COLUMN workspace_id uuid;

DO $$
DECLARE
  live uuid := (SELECT id FROM workspace WHERE archived_at IS NULL ORDER BY created_at LIMIT 1);
  t    text;
BEGIN
  FOREACH t IN ARRAY ARRAY[
    'passport', 'oauth_client', 'oauth_grant',
    'oauth_authorization_code', 'oauth_refresh_token', 'auth_token'
  ] LOOP
    EXECUTE format('UPDATE %I SET workspace_id = $1 WHERE workspace_id IS NULL', t) USING live;
    EXECUTE format('ALTER TABLE %I ALTER COLUMN workspace_id SET NOT NULL', t);
  END LOOP;
END $$;

ALTER TABLE passport ADD CONSTRAINT passport_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE oauth_client ADD CONSTRAINT oauth_client_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE oauth_grant ADD CONSTRAINT oauth_grant_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE oauth_authorization_code ADD CONSTRAINT oauth_authorization_code_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE oauth_refresh_token ADD CONSTRAINT oauth_refresh_token_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE auth_token ADD CONSTRAINT auth_token_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;

DROP INDEX idx_passport_obo;
DROP INDEX passport_oauth_grant_ix;
DROP INDEX oauth_grant_user_live_ix;
DROP INDEX oauth_grant_lent_passport_ix;
DROP INDEX oauth_code_lent_passport_ix;
DROP INDEX oauth_refresh_token_grant_ix;
DROP INDEX idx_auth_token_user;

CREATE INDEX idx_passport_obo ON passport (workspace_id, on_behalf_of) WHERE revoked_at IS NULL;
CREATE INDEX passport_oauth_grant_ix ON passport (workspace_id, oauth_grant_id);
CREATE INDEX oauth_grant_user_live_ix ON oauth_grant (workspace_id, user_id, id) WHERE revoked_at IS NULL;
CREATE INDEX oauth_grant_lent_passport_ix ON oauth_grant (workspace_id, lent_passport_id);
CREATE INDEX oauth_code_lent_passport_ix ON oauth_authorization_code (workspace_id, lent_passport_id);
CREATE INDEX oauth_refresh_token_grant_ix ON oauth_refresh_token (workspace_id, grant_id);
CREATE INDEX idx_auth_token_user ON auth_token (workspace_id, user_id, purpose) WHERE used_at IS NULL;
