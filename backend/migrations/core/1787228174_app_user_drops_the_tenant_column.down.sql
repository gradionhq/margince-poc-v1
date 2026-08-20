-- Reverse of the phase D drop on app_user and session.
--
-- The column comes back bound to the installation's own workspace, which is the
-- only LIVE one a single-organization installation has (ADR-0061) and therefore
-- the one every restored row belonged to. The shape is restored, not a per-row
-- reconstruction: nothing else records which workspace a user was in.
--
-- `archived_at IS NULL` is load-bearing here in a way it is not on the sibling
-- phase D downs. An installation that merged an archived predecessor still
-- carries its row, and it can be the OLDER one; these two tables hold login
-- credentials and live sessions, so binding them to a dead tenant would lock
-- every user out of the installation they belong to. InstallationWorkspace
-- resolves the live one, and this matches it.
SET LOCAL lock_timeout = '3s';

ALTER TABLE app_user ADD COLUMN workspace_id uuid;
UPDATE app_user SET workspace_id =
  (SELECT id FROM workspace WHERE archived_at IS NULL ORDER BY created_at LIMIT 1);
ALTER TABLE app_user
  ALTER COLUMN workspace_id SET NOT NULL,
  ADD CONSTRAINT app_user_workspace_id_fkey
    FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT,
  -- COMPOSITE, which is the shape core 0019 created and the shape core 0218's
  -- own down half needs to point its restored foreign keys at. Collapsing it to
  -- UNIQUE (id) is a CUSTOM migration's job (20260813130000), and custom is not
  -- part of a core rollback — so restoring the collapsed shape here would leave
  -- 0218 with no unique constraint to reference.
  ADD CONSTRAINT uq_app_user_ws_id UNIQUE (workspace_id, id);

ALTER TABLE session ADD COLUMN workspace_id uuid;
UPDATE session SET workspace_id =
  (SELECT id FROM workspace WHERE archived_at IS NULL ORDER BY created_at LIMIT 1);
ALTER TABLE session
  ALTER COLUMN workspace_id SET NOT NULL,
  ADD CONSTRAINT session_workspace_id_fkey
    FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;

CREATE INDEX idx_app_user_ws ON app_user (workspace_id) WHERE archived_at IS NULL;
DROP INDEX idx_app_user_live;

CREATE INDEX idx_session_user_wide ON session (workspace_id, user_id) WHERE revoked_at IS NULL;
DROP INDEX idx_session_user;
ALTER INDEX idx_session_user_wide RENAME TO idx_session_user;
