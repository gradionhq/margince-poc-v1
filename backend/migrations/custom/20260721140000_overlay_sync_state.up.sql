-- 20260721140000_overlay_sync_state: the overlay poller's per-workspace
-- scheduling sidecar (branch 1b, mirroring capture's CAP-DDL-5 sync-state).
-- Without it a workspace whose incumbent sweep keeps failing — a revoked
-- token, an unreachable HubSpot, a rate-limit — is re-selected by
-- DueOverlayConnections every tick and re-swept hot, hammering a dead or
-- throttled connection. This row gates the next sweep: a failing sweep
-- backs off (2min·2^n capped at 4h, jittered; rate-limits honor a longer
-- floor), and one clean sweep resets it. Scheduling state has a live reader
-- (the due-scan), so it earns columns; error DETAIL stays in system_log —
-- the row carries only the class.
--
-- Keyed on workspace_id, not connection_id: an overlay has exactly one
-- active incumbent_connection per workspace (incumbent_connection's
-- UNIQUE(workspace_id)), and disconnect teardown purges this row, so a
-- reconnect always starts fresh (no stale backoff carried across).
CREATE TABLE overlay_sync_state (
  workspace_id uuid PRIMARY KEY REFERENCES workspace(id) ON DELETE RESTRICT,
  next_sweep_at timestamptz NOT NULL DEFAULT now(),
  consecutive_failures int NOT NULL DEFAULT 0,
  -- rate_limited (honor a longer floor) and auth (persistent — needs its
  -- human) are the schedulable distinctions the overlay package can detect
  -- from the apperrors sentinels a sweep surfaces; every other transient
  -- failure (including transport-unreachable, which is a hubspot-package
  -- sentinel this package cannot import without a cycle) is internal.
  last_error_class text NULL CHECK (last_error_class IS NULL OR
    last_error_class IN ('rate_limited','auth','internal')),
  last_success_at timestamptz NULL,
  last_failure_at timestamptz NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_overlay_sync_due ON overlay_sync_state (next_sweep_at);

ALTER TABLE overlay_sync_state ENABLE ROW LEVEL SECURITY;
ALTER TABLE overlay_sync_state FORCE ROW LEVEL SECURITY;
CREATE POLICY overlay_sync_state_tenant_isolation ON overlay_sync_state
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
