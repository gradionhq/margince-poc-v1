-- 20260717090000_overlay_reconcile_watermark: the persisted incremental
-- watermark Reconcile (overlay/reconcile.go) advances after
-- every sweep pass — distinct from overlay_backfill_cursor's list-cursor
-- (an id-keyset offset into the LIST endpoint, done once), this is a
-- timestamp watermark into the incumbent's modified-since Search sweep
-- that runs forever (design.md §4.4: "Pull always runs ... always-on
-- backup"). One row per (workspace, incumbent object class); a restart
-- resumes the sweep from the last-persisted watermark rather than
-- re-walking every record since epoch.
CREATE TABLE overlay_reconcile_watermark (
  workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  object_class text NOT NULL,
  watermark timestamptz NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (workspace_id, object_class)
);

ALTER TABLE overlay_reconcile_watermark ENABLE ROW LEVEL SECURITY;
ALTER TABLE overlay_reconcile_watermark FORCE ROW LEVEL SECURITY;
CREATE POLICY overlay_reconcile_watermark_tenant_isolation ON overlay_reconcile_watermark
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
