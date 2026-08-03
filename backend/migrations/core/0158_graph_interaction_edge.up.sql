-- 0158: the user↔contact interaction edge (CG-DDL-1 / ADR-0078).
--
-- Which of our users interacts with which contact, how much, how recently.
-- Everything here is derived from activity_participant (0157); the table holds
-- no fact of its own. That is what makes it a PROJECTION rather than a record,
-- and why it carries no id, no version, no audit row and no outbox row — the
-- embedding-store precedent. Provenance and audit live in the base tables it
-- is folded from, and a full rebuild is the corruption remedy.
--
-- It exists because the alternative is deriving the edge on every read, which
-- is a scan of the whole timeline per page view.
--
-- WHAT IS DELIBERATELY NOT HERE: the 0–100 strength. It is a pure function of
-- (row, now) computed at read. A decayed score is wrong the moment the clock
-- moves, and storing it would mean either a lie or a nightly job rewriting
-- every row in the table to change nothing but a number anyone can derive.

CREATE TABLE graph_interaction_edge (
  workspace_id     uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  user_id          uuid NOT NULL,
  person_id        uuid NOT NULL,

  -- Exact, and the dominant term in the score.
  last_at          timestamptz NOT NULL,
  -- Direction matters on its own: a contact who only ever receives from us is
  -- not a relationship, however often we write. NULL means no interaction in
  -- that direction has ever been recorded, which is not the same as "long ago".
  last_inbound_at  timestamptz NULL,
  last_outbound_at timestamptz NULL,

  -- BOUNDED-STALE BY CONTRACT, and this is the honest part. An activity ageing
  -- past the 90-day boundary changes these counts without anything happening
  -- to write a row, so they are only re-trued at the nightly reconcile: a
  -- count may be up to 24h over-inclusive. Recency above is exact, and it
  -- dominates the score, so the drift is invisible in the number and stated
  -- rather than hidden.
  count_90d        integer NOT NULL DEFAULT 0,
  in_count_90d     integer NOT NULL DEFAULT 0,
  out_count_90d    integer NOT NULL DEFAULT 0,
  count_total      integer NOT NULL DEFAULT 0,
  computed_at      timestamptz NOT NULL DEFAULT now(),

  PRIMARY KEY (workspace_id, user_id, person_id),
  -- Composite tenant-local FKs: a bare FK is checked as the table owner and
  -- bypasses RLS. CASCADE both ways — an edge to a deleted person or a deleted
  -- user is not a fact, and the projection is rebuildable anyway.
  FOREIGN KEY (workspace_id, user_id)   REFERENCES app_user (workspace_id, id) ON DELETE CASCADE,
  FOREIGN KEY (workspace_id, person_id) REFERENCES person   (workspace_id, id) ON DELETE CASCADE
);

-- The two questions the surfaces ask: "who on our team knows this contact"
-- (person-anchored, warmest first) and "who does this colleague know"
-- (user-anchored). last_at DESC is in the index because both are ranked reads
-- with a cap, never full scans.
CREATE INDEX idx_graph_edge_person ON graph_interaction_edge (workspace_id, person_id, last_at DESC);
CREATE INDEX idx_graph_edge_user   ON graph_interaction_edge (workspace_id, user_id, last_at DESC);

ALTER TABLE graph_interaction_edge ENABLE ROW LEVEL SECURITY;
ALTER TABLE graph_interaction_edge FORCE ROW LEVEL SECURITY;
CREATE POLICY graph_interaction_edge_tenant_isolation ON graph_interaction_edge
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

GRANT SELECT, INSERT, UPDATE, DELETE ON graph_interaction_edge TO margince_app;
