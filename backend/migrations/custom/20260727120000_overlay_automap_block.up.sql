-- One row = "an admin deliberately unmapped this user; automatic email
-- matching must not map them again." Without it, an admin's unmap is
-- re-created by the next reconcile sweep whenever the user's email still
-- matches an incumbent owner, so the delete silently undoes itself.
--
-- A separate table rather than a third mirror_user_map.match_source value:
-- mirror_user_map.incumbent_user_id is NOT NULL, so "maps to nobody,
-- deliberately" has no representable row there, and a sentinel empty string
-- would make a non-mapping indistinguishable from a mapping to every reader.
CREATE TABLE mirror_user_automap_block (
  workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  app_user_id  uuid NOT NULL,
  incumbent    text NOT NULL,
  blocked_at   timestamptz NOT NULL DEFAULT now(),
  blocked_by   uuid NOT NULL,
  PRIMARY KEY (workspace_id, app_user_id, incumbent),
  -- Composite FK (workspace_id, app_user_id), not a bare app_user_id: a
  -- tenant-local FK carries workspace_id on both sides so a cross-workspace
  -- target is rejected by the database, not merely hidden by RLS row
  -- visibility (the same pattern mirror_user_map's own FK uses).
  CONSTRAINT mirror_user_automap_block_app_user_id_fkey
    FOREIGN KEY (workspace_id, app_user_id)
    REFERENCES app_user (workspace_id, id) ON DELETE CASCADE,
  -- ON DELETE RESTRICT, not CASCADE: blocked_by is the accountability half of
  -- the row — the admin who decided this user must stay unmapped. Removing
  -- that admin must not silently erase the decision (nor the record of who
  -- made it), which is the same posture passport.granted_by and
  -- record_grant.granted_by take for their NOT NULL actor columns.
  CONSTRAINT mirror_user_automap_block_blocked_by_fkey
    FOREIGN KEY (workspace_id, blocked_by)
    REFERENCES app_user (workspace_id, id) ON DELETE RESTRICT
);

ALTER TABLE mirror_user_automap_block ENABLE ROW LEVEL SECURITY;
ALTER TABLE mirror_user_automap_block FORCE ROW LEVEL SECURITY;
CREATE POLICY mirror_user_automap_block_tenant_isolation ON mirror_user_automap_block
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
