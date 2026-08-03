ALTER TABLE oauth_client DROP COLUMN IF EXISTS last_used_at;
ALTER TABLE oauth_client DROP COLUMN IF EXISTS created_via;
ALTER TABLE oauth_client DROP COLUMN IF EXISTS deleted_at;
ALTER TABLE oauth_client DROP COLUMN IF EXISTS disabled_at;

-- The passport reference goes before the table it points at.
DROP INDEX IF EXISTS passport_oauth_grant_ix;
ALTER TABLE passport DROP CONSTRAINT IF EXISTS passport_grant_fkey;
ALTER TABLE passport DROP COLUMN IF EXISTS last_used_at;
ALTER TABLE passport DROP COLUMN IF EXISTS oauth_grant_id;

DROP INDEX IF EXISTS oauth_refresh_token_grant_ix;
DROP TABLE IF EXISTS oauth_refresh_token;
DROP INDEX IF EXISTS oauth_grant_user_live_ix;
DROP TABLE IF EXISTS oauth_grant;
