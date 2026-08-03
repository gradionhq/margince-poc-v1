DROP INDEX IF EXISTS oauth_grant_lent_passport_ix;
DROP INDEX IF EXISTS oauth_code_lent_passport_ix;
ALTER TABLE oauth_grant DROP CONSTRAINT IF EXISTS oauth_grant_lent_passport_fkey;
ALTER TABLE oauth_authorization_code DROP CONSTRAINT IF EXISTS oauth_code_lent_passport_fkey;
ALTER TABLE oauth_grant DROP COLUMN IF EXISTS lent_passport_id;
ALTER TABLE oauth_authorization_code DROP COLUMN IF EXISTS lent_passport_id;
