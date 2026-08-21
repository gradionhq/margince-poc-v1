-- ADR-0091 §8 phase D: app_user and session drop the tenant column.
--
-- A single-organization installation has one workspace (ADR-0061), and the
-- server already resolves it per request without consulting either table:
-- identity's middleware calls InstallationWorkspace before any user is looked
-- up. app_user.workspace_id was a second source for a value already
-- established, and email identifies an account on its own — uq_app_user_email
-- is UNIQUE (lower(email)) across the installation, not per workspace.
--
-- uq_app_user_ws_id goes with it: a phase B leftover collapsed to UNIQUE (id),
-- which app_user_pkey already says.
--
-- Both partial indexes LEAD with the column, so they are replaced rather than
-- left to fall with it: DROP COLUMN drops a multi-column index outright.
--
-- Each replacement is keyed to the read it actually serves, which is not what
-- the dropped pair served. idx_app_user_live takes (created_at, id) because the
-- roster's page is a keyset on exactly that (identity/roster.go's
-- listUsersQuery) — (id) alone would duplicate the primary key and serve no
-- ordering. idx_session_user keeps (user_id) for the revoke-every-session-for-
-- this-user sweep (identity/users.go); the per-request lookup is on token_hash
-- and is served by that column's own UNIQUE constraint, not by this index.
--
-- Narrow built before wide is dropped, so no read is served without one.
--
-- SET LOCAL lock_timeout: these take ACCESS EXCLUSIVE locks on the two tables
-- every authenticated request touches, and an unbounded wait would queue behind
-- one open transaction for as long as it lives.
SET LOCAL lock_timeout = '3s';

CREATE INDEX idx_app_user_live ON app_user (created_at, id) WHERE archived_at IS NULL;
DROP INDEX idx_app_user_ws;

CREATE INDEX idx_session_user_narrow ON session (user_id) WHERE revoked_at IS NULL;
DROP INDEX idx_session_user;
ALTER INDEX idx_session_user_narrow RENAME TO idx_session_user;

ALTER TABLE app_user DROP CONSTRAINT uq_app_user_ws_id;

ALTER TABLE app_user DROP COLUMN workspace_id;
ALTER TABLE session DROP COLUMN workspace_id;
