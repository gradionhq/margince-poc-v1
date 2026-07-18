-- CAP-DDL-4 (ADR-0063): the bounded connect-time backfill. Its own table,
-- deliberately not columns on capture_connection: one live run per
-- connection, its own backward-paging provider cursor (never sync_cursor —
-- incremental moves forward from the connect-time watermark, so the two
-- interleave without conflict), and counter columns that make the activation
-- read a single indexed row (CAP-PARAM-2 < 150ms). Resumable: a worker death
-- resumes from the last committed cursor; cancel retains captured rows.

CREATE TABLE capture_backfill (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id  uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  connection_id uuid NOT NULL,
  CONSTRAINT capture_backfill_connection_fkey
    FOREIGN KEY (workspace_id, connection_id)
    REFERENCES capture_connection (workspace_id, id) ON DELETE CASCADE,

  window_months int  NOT NULL CHECK (window_months IN (3, 6, 12)),  -- CAP-PARAM-4 ('none' = no row)
  after_date    date NOT NULL,  -- the frozen window boundary, computed at start

  status        text NOT NULL DEFAULT 'queued'
                CHECK (status IN ('queued','running','done','error','cancelled')),
  cursor        jsonb NULL,     -- provider page token — backward-paging, disjoint from sync_cursor

  total_estimate int NULL,      -- the previewed count the user consented to (progress denominator)
  scanned       int NOT NULL DEFAULT 0,  -- counters: one UPDATE per committed page
  captured      int NOT NULL DEFAULT 0,
  skipped       int NOT NULL DEFAULT 0,
  people_created        int NOT NULL DEFAULT 0,
  organizations_created int NOT NULL DEFAULT 0,
  dedupe_candidates     int NOT NULL DEFAULT 0,

  started_at    timestamptz NULL,
  completed_at  timestamptz NULL,
  last_error_class text NULL,   -- class only; detail is system_log's (the 0078 rationale)

  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now()
);

-- One live backfill per connection; widen-only re-invokes create a new row
-- for the delta once the prior run is terminal.
CREATE UNIQUE INDEX uq_capture_backfill_live ON capture_backfill (connection_id)
  WHERE status IN ('queued','running');
CREATE INDEX idx_capture_backfill_conn ON capture_backfill (workspace_id, connection_id, created_at DESC);

CREATE TRIGGER trg_capture_backfill_updated BEFORE UPDATE ON capture_backfill
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();

ALTER TABLE capture_backfill ENABLE ROW LEVEL SECURITY;
ALTER TABLE capture_backfill FORCE ROW LEVEL SECURITY;
CREATE POLICY capture_backfill_tenant_isolation ON capture_backfill
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
