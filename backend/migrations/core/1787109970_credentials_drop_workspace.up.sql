-- 1787109970: the credential tables drop the tenant column (ADR-0091 §8 phase D).
--
-- Six tables, all of them about a credential rather than a record:
--
--   passport                  an agent seat's delegated credential
--   oauth_client              a registered client (DCR or configured)
--   oauth_grant               a human's standing consent to one client
--   oauth_authorization_code  the one-shot code between consent and token
--   oauth_refresh_token       a grant's renewable half
--   auth_token                password reset / invite, one purpose per row
--
-- These go before app_user and session deliberately. Every one hangs off a USER
-- rather than off the tenant — the subject a passport acts for, the human a
-- grant belongs to, the recipient a reset names — so the foreign key already
-- carries the bound the tenant column was restating, and none of the six is
-- what MustWorkspace resolves through.
--
-- Two reads used to take Identity.WorkspaceID off the credential row itself
-- (the refresh-token lock and the authorization-code redemption). They now take
-- it from app_user, which still carries the column and is the credential's own
-- subject: the same value by a shorter path, and no new authority. When
-- app_user loses the column in the last slice of this group, that is the point
-- at which the installation has to resolve itself — deliberately NOT decided
-- here, where it would be decided by whichever spelling compiled.
--
-- None carries a phase-B leftover constraint: their keys already name something
-- narrower than the tenant, and the only thing the column still held was its
-- foreign key to workspace.

ALTER TABLE auth_token DROP CONSTRAINT auth_token_workspace_id_fkey;
ALTER TABLE auth_token DROP COLUMN workspace_id;

ALTER TABLE oauth_refresh_token DROP CONSTRAINT oauth_refresh_token_workspace_id_fkey;
ALTER TABLE oauth_refresh_token DROP COLUMN workspace_id;

ALTER TABLE oauth_authorization_code DROP CONSTRAINT oauth_authorization_code_workspace_id_fkey;
ALTER TABLE oauth_authorization_code DROP COLUMN workspace_id;

ALTER TABLE oauth_grant DROP CONSTRAINT oauth_grant_workspace_id_fkey;
ALTER TABLE oauth_grant DROP COLUMN workspace_id;

ALTER TABLE oauth_client DROP CONSTRAINT oauth_client_workspace_id_fkey;
ALTER TABLE oauth_client DROP COLUMN workspace_id;

ALTER TABLE passport DROP CONSTRAINT passport_workspace_id_fkey;
ALTER TABLE passport DROP COLUMN workspace_id;

-- The indexes that led with the column, recreated on what actually selects
-- rows: the subject a passport acts for, the grant a token renews, the user a
-- reset names. Every predicate carries over unchanged — what these serve did
-- not change, only what they had to seek past to get there.
CREATE INDEX idx_passport_obo ON passport (on_behalf_of) WHERE revoked_at IS NULL;
CREATE INDEX passport_oauth_grant_ix ON passport (oauth_grant_id);

CREATE INDEX oauth_grant_user_live_ix ON oauth_grant (user_id, id) WHERE revoked_at IS NULL;
CREATE INDEX oauth_grant_lent_passport_ix ON oauth_grant (lent_passport_id);

CREATE INDEX oauth_code_lent_passport_ix ON oauth_authorization_code (lent_passport_id);

CREATE INDEX oauth_refresh_token_grant_ix ON oauth_refresh_token (grant_id);

CREATE INDEX idx_auth_token_user ON auth_token (user_id, purpose) WHERE used_at IS NULL;
