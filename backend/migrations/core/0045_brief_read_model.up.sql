-- 0045: Morning-Brief read model (B-E05.3b, data-model §12.5). Two
-- tables: `brief_run` — one persisted ranking run per rep, carrying the
-- data cutoff (`as_of`) the next run derives "changed overnight" from,
-- plus the queue-snapshot metadata (candidate count, the REVENUE_NORM
-- the composite folded with) that makes a run reproducible after the
-- fact — and `brief_item`, one ranked queue entry: the §10.1 composite,
-- its feature vector, the evidence ids behind every factor
-- (evidence-or-omit, B-E05.12), and the per-rep acted/dismissed state
-- (B-E05.13) whose `state_at` is the changed-since cursor.
CREATE TABLE brief_run (
  id                 uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id       uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  user_id            uuid NOT NULL,
  generated_at       timestamptz NOT NULL DEFAULT now(),
  as_of              timestamptz NOT NULL,                  -- the data cutoff this brief reflects; ~30-day retention (config)
  candidate_count    integer NOT NULL CHECK (candidate_count >= 0),
  revenue_norm_minor bigint NOT NULL CHECK (revenue_norm_minor > 0),  -- the P90 (or fallback) the revenue factor used
  CONSTRAINT uq_brief_run_ws_id UNIQUE (workspace_id, id),
  CONSTRAINT brief_run_user_fkey FOREIGN KEY (workspace_id, user_id)
    REFERENCES app_user (workspace_id, id) ON DELETE CASCADE
);
CREATE INDEX idx_brief_run_user ON brief_run (workspace_id, user_id, generated_at DESC);

ALTER TABLE brief_run ENABLE ROW LEVEL SECURITY;
ALTER TABLE brief_run FORCE ROW LEVEL SECURITY;
CREATE POLICY brief_run_tenant_isolation ON brief_run
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

CREATE TABLE brief_item (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id   uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  brief_run_id   uuid NOT NULL,
  deal_id        uuid NOT NULL,
  rank           integer NOT NULL CHECK (rank >= 1),
  composite      double precision NOT NULL CHECK (composite >= 0 AND composite <= 1),
  feature_vector jsonb NOT NULL,                            -- the §10.1 factor decomposition (no mystery number)
  evidence_ids   uuid[] NOT NULL,                           -- the source rows behind the factors (B-E05.12)
  state          text NOT NULL DEFAULT 'new' CHECK (state IN ('new','acted','dismissed')),
  state_at       timestamptz NULL,                          -- changed-since cursor = (user via run, state_at)
  -- A mark without its instant (or an instant without a mark) would
  -- break the changed-since cursor and the re-eligibility comparison.
  CONSTRAINT brief_item_state_stamped CHECK ((state = 'new') = (state_at IS NULL)),
  CONSTRAINT brief_item_run_fkey FOREIGN KEY (workspace_id, brief_run_id)
    REFERENCES brief_run (workspace_id, id) ON DELETE CASCADE,
  CONSTRAINT brief_item_deal_fkey FOREIGN KEY (workspace_id, deal_id)
    REFERENCES deal (workspace_id, id) ON DELETE CASCADE,
  CONSTRAINT uq_brief_item_run_deal UNIQUE (brief_run_id, deal_id),
  CONSTRAINT uq_brief_item_run_rank UNIQUE (brief_run_id, rank)
);
CREATE INDEX idx_brief_item_run   ON brief_item (brief_run_id, rank);
CREATE INDEX idx_brief_item_state ON brief_item (brief_run_id, state, state_at);
-- The next run's acted/dismissed exclusion looks up marks per deal.
CREATE INDEX idx_brief_item_deal  ON brief_item (workspace_id, deal_id) WHERE state <> 'new';

ALTER TABLE brief_item ENABLE ROW LEVEL SECURITY;
ALTER TABLE brief_item FORCE ROW LEVEL SECURITY;
CREATE POLICY brief_item_tenant_isolation ON brief_item
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
