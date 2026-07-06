-- 0049: saved_view (B-E15.12, data-model §12.5) — per-user column/sort/
-- filter view state for a list surface. V1 is per-user (P1-exempt,
-- runtime-config-surface.md §3): a view is owned by one app_user and read
-- back only by that owner; shared/team views are a fast-follow, so
-- shared_scope is carried for the schema but the store enforces 'private'.
-- The query jsonb is the saved filter/sort/columns using the §13.5
-- vocabulary; it is persisted verbatim so a save→reload restores it
-- exactly, and validated by the ONE predicate engine when it is executed
-- as a list/export (B-E15.13), not re-shaped here.
CREATE TABLE saved_view (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id  uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  owner_id      uuid NOT NULL,
  shared_scope  text NOT NULL DEFAULT 'private' CHECK (shared_scope IN ('private','team','workspace')),
  resource      text NOT NULL CHECK (resource IN ('people','organizations','deals','activities','leads','partners')),
  name          text NOT NULL,
  query         jsonb NOT NULL,
  version       bigint NOT NULL DEFAULT 1,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  archived_at   timestamptz NULL,
  -- Composite tenant FK so a view can never point at an owner in another
  -- workspace (schema_fitness composite-FK invariant).
  CONSTRAINT saved_view_owner_fkey FOREIGN KEY (workspace_id, owner_id)
    REFERENCES app_user (workspace_id, id) ON DELETE CASCADE
);
CREATE INDEX idx_saved_view_owner ON saved_view (workspace_id, owner_id, resource) WHERE archived_at IS NULL;

CREATE TRIGGER trg_saved_view_updated BEFORE UPDATE ON saved_view
  FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

ALTER TABLE saved_view ENABLE ROW LEVEL SECURITY;
ALTER TABLE saved_view FORCE ROW LEVEL SECURITY;
CREATE POLICY saved_view_tenant_isolation ON saved_view
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
