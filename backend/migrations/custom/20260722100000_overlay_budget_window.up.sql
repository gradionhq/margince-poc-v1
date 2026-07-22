-- 20260722100000_overlay_budget_window: the OVB (overlay budget) meter's
-- shared, cross-process fixed-window counter (branch 1b, A3b).
--
-- The meter was per-PROCESS in-memory: cmd/api (the force_fresh lane) and
-- cmd/worker (the poller lane) are two distinct OS processes (ADR-0054/A69),
-- each with its OWN counter, so a workspace's combined spend against the ONE
-- real HubSpot quota was bounded by (process count)×Limit, not Limit — the
-- "don't starve the shared quota" property undercut by construction. This
-- table makes the window shared: both lanes, in both processes, read and
-- advance ONE row per workspace, and GET /overlay/budget reflects the poller's
-- spend, not just the reading process's.
--
-- The window is fixed (not sliding): window_start plus a configured Window
-- duration; a consume/reserve whose "now" is past window_start+Window rolls
-- the window (resets window_start and consumed) in the same statement, so a
-- stale window can never over-count. "now" is supplied by the caller (the
-- meter's injected clock), not SQL now(), so window expiry stays testable
-- without sleeping. consumed is the total across lanes (the band the meter
-- reports is a function of the total); per-lane detail is not persisted.
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
