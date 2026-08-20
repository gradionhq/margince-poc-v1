-- Reverse of the phase D drop on role and role_assignment.
--
-- The column comes back bound to the installation's own workspace, which is the
-- only one a single-organization installation has (ADR-0061) and therefore the
-- one every restored row belonged to. This is the shape restored, not a per-row
-- reconstruction: nothing else records which workspace a role was for.
SET LOCAL lock_timeout = '3s';

ALTER TABLE role ADD COLUMN workspace_id uuid;
UPDATE role SET workspace_id = (SELECT id FROM workspace ORDER BY created_at LIMIT 1);
ALTER TABLE role
  ALTER COLUMN workspace_id SET NOT NULL,
  ADD CONSTRAINT role_workspace_id_fkey
    FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT,
  ADD CONSTRAINT uq_role_ws_id UNIQUE (id);

ALTER TABLE role_assignment ADD COLUMN workspace_id uuid;
UPDATE role_assignment SET workspace_id = (SELECT id FROM workspace ORDER BY created_at LIMIT 1);
ALTER TABLE role_assignment
  ALTER COLUMN workspace_id SET NOT NULL,
  ADD CONSTRAINT role_assignment_workspace_id_fkey
    FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
