-- ADR-0072/A118 (CAP-PARAM-7): the captured-organization auto-enrich sweep's
-- per-org attempt cursor and the per-workspace daily spend cap.
--
-- The sweep (run-on-start + daily) enqueues a governed deep-read for every
-- surviving auto-created organization that has a domain and no dossier yet,
-- when the workspace's capture_auto_enrich flag is on, up to a daily cap. Both
-- tables are operational scheduling state with a live reader (the sweep's
-- due-scan), so they earn columns; error detail still belongs to system_log.

-- capture_auto_enrich_state: one row per organization the sweep has considered.
-- The next_attempt_at due-scan key drives bounded retries with backoff; an org
-- with no row is due immediately (the sweep left-joins).
CREATE TABLE capture_auto_enrich_state (
  organization_id uuid PRIMARY KEY,
  workspace_id    uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  -- The composite-FK convention (0019): the tenant-local reference carries
  -- workspace_id so a cross-workspace target is rejected by the database.
  CONSTRAINT capture_auto_enrich_state_organization_id_fkey
    FOREIGN KEY (workspace_id, organization_id)
    REFERENCES organization (workspace_id, id) ON DELETE CASCADE,

  attempts        int NOT NULL DEFAULT 0,
  last_attempt_at timestamptz NULL,
  -- When the sweep may next consider this org. Cleared to NULL when the read
  -- terminally resolves (a dossier applied), or when the sweep's per-pass
  -- ExpireExhausted retires an org that used every attempt without success —
  -- either way the row drops out of the partial due-index below and never
  -- re-enqueues.
  next_attempt_at timestamptz NULL DEFAULT now(),
  last_outcome    text NULL CHECK (last_outcome IS NULL OR
    last_outcome IN ('queued', 'applied', 'empty', 'failed', 'exhausted')),

  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_capture_auto_enrich_due ON capture_auto_enrich_state (next_attempt_at)
  WHERE next_attempt_at IS NOT NULL;

ALTER TABLE capture_auto_enrich_state ENABLE ROW LEVEL SECURITY;
ALTER TABLE capture_auto_enrich_state FORCE ROW LEVEL SECURITY;
CREATE POLICY capture_auto_enrich_state_tenant_isolation ON capture_auto_enrich_state
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

-- capture_auto_enrich_budget: the per-workspace, per-UTC-day reservation counter
-- (CAP-PARAM-7 daily cap). The sweep reserves a slot atomically before each
-- enqueue; a day with no row has spent nothing. Old days are harmless residue,
-- pruned opportunistically by the sweep, never read for a past date.
CREATE TABLE capture_auto_enrich_budget (
  workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  budget_date  date NOT NULL,
  enqueued     int  NOT NULL DEFAULT 0,
  PRIMARY KEY (workspace_id, budget_date)
);

ALTER TABLE capture_auto_enrich_budget ENABLE ROW LEVEL SECURITY;
ALTER TABLE capture_auto_enrich_budget FORCE ROW LEVEL SECURITY;
CREATE POLICY capture_auto_enrich_budget_tenant_isolation ON capture_auto_enrich_budget
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
