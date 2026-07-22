-- Reverse: recreate the (now-unused) OVB budget-window table exactly as
-- 20260722100000 first created it, so migrate down|up stays symmetric. The
-- meter no longer reads or writes it; this exists only so this migration
-- reverses cleanly.
CREATE TABLE overlay_budget_window (
  workspace_id uuid PRIMARY KEY REFERENCES workspace(id) ON DELETE RESTRICT,
  window_start timestamptz NOT NULL,
  consumed     int NOT NULL DEFAULT 0 CHECK (consumed >= 0),
  updated_at   timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE overlay_budget_window ENABLE ROW LEVEL SECURITY;
ALTER TABLE overlay_budget_window FORCE ROW LEVEL SECURITY;
CREATE POLICY overlay_budget_window_tenant_isolation ON overlay_budget_window
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
