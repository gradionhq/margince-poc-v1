-- AMENDED (ADR-0091 §8 phase D): the composite foreign keys below referenced
-- app_user (workspace_id, id) and now reference app_user (id). `dbmigrate.Up`
-- applies ALL of `core` before ANY of `custom`, so on a FRESH database this
-- file runs against the FINAL core schema — where app_user has no workspace_id
-- and no UNIQUE (workspace_id, id) for a composite key to point at — and the
-- install fails outright. Amending is what keeps a fresh install working.
--
-- The tenancy integrity the composite form bought is not lost, it has no
-- subject: a single-organization installation (ADR-0061) has one workspace, so
-- there is no cross-workspace target for the database to reject. This is the
-- same rewrite 20260813120000_single_column_foreign_keys_custom already applies
-- to these constraints at a later version; a deployed database reaches the same
-- shape either way.
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
  -- This carried workspace_id on both sides so a cross-workspace target was
  -- rejected by the database rather than merely hidden by RLS; see the
  -- amendment note at the top of this file.
  CONSTRAINT mirror_user_automap_block_app_user_id_fkey
    FOREIGN KEY (app_user_id)
    REFERENCES app_user (id) ON DELETE CASCADE,
  -- ON DELETE RESTRICT, not CASCADE: blocked_by is the accountability half of
  -- the row — the admin who decided this user must stay unmapped. Removing
  -- that admin must not silently erase the decision (nor the record of who
  -- made it), which is the same posture passport.granted_by and
  -- record_grant.granted_by take for their NOT NULL actor columns.
  CONSTRAINT mirror_user_automap_block_blocked_by_fkey
    FOREIGN KEY (blocked_by)
    REFERENCES app_user (id) ON DELETE RESTRICT
);

ALTER TABLE mirror_user_automap_block ENABLE ROW LEVEL SECURITY;
ALTER TABLE mirror_user_automap_block FORCE ROW LEVEL SECURITY;
CREATE POLICY mirror_user_automap_block_tenant_isolation ON mirror_user_automap_block
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
