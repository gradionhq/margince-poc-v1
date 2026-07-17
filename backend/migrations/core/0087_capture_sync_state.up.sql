-- CAP-DDL-5 (ADR-0063): the sweep's per-connection scheduling sidecar.
-- Scheduling state has a live reader (the dispatcher's due-scan) so it earns
-- columns; error DETAIL still belongs to system_log — the row carries only the
-- class (narrowing, not reversing, 0078's no-diagnostics rationale on
-- capture_connection). Rows are upserted lazily on a connection's first sync;
-- a connection with no row is due immediately.

CREATE TABLE capture_sync_state (
  connection_id uuid PRIMARY KEY REFERENCES capture_connection(id) ON DELETE CASCADE,
  workspace_id  uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,

  -- The due-scan key. Rate limits honor Retry-After; other transient errors
  -- back off 2min·2^n capped at 4h, jittered. A connection in status 'error'
  -- is probed daily — degraded, never tombstoned (one success flips it back).
  next_sync_at  timestamptz NOT NULL DEFAULT now(),

  consecutive_failures int NOT NULL DEFAULT 0,
  last_synced_at   timestamptz NULL,
  last_success_at  timestamptz NULL,
  last_error_class text NULL CHECK (last_error_class IS NULL OR
    last_error_class IN ('rate_limited','unreachable','auth','history_gone','internal')),

  -- Politeness flag while a backfill pages the same mailbox: the sweep widens
  -- its jitter. Correctness needs nothing — the two cursors are disjoint.
  backfill_active boolean NOT NULL DEFAULT false,

  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_capture_sync_due ON capture_sync_state (next_sync_at);

CREATE TRIGGER trg_capture_sync_state_updated BEFORE UPDATE ON capture_sync_state
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE capture_sync_state ENABLE ROW LEVEL SECURITY;
ALTER TABLE capture_sync_state FORCE ROW LEVEL SECURITY;
CREATE POLICY capture_sync_state_tenant_isolation ON capture_sync_state
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
