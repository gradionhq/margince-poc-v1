-- 20260716140000_overlay_backfill_cursor: the resumable list-cursor
-- checkpoint Backfill (overlay/backfill.go) persists after
-- every page — a restart resumes from the last-saved cursor instead of
-- re-listing from the incumbent's start (design.md §4.4: "Backfill:
-- checkpointed, resumable, list-cursor paginated per object"). One row
-- per (workspace, incumbent object class); `done` retires a converged
-- backfill so a later call is a cheap no-op rather than a re-list.
CREATE TABLE overlay_backfill_cursor (
  workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  object_class text NOT NULL,
  cursor text NOT NULL DEFAULT '',
  done boolean NOT NULL DEFAULT false,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (workspace_id, object_class)
);

ALTER TABLE overlay_backfill_cursor ENABLE ROW LEVEL SECURITY;
ALTER TABLE overlay_backfill_cursor FORCE ROW LEVEL SECURITY;
CREATE POLICY overlay_backfill_cursor_tenant_isolation ON overlay_backfill_cursor
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
