-- The per-user "I have seen this record" baseline behind the company view's
-- since-last-visit counts, plus the index the org-filtered signals read needs.
--
-- user_record_view is a read-model table, not a record fact: it holds one
-- user's view state, it is written on every visit, and no other user (and no
-- report) may read it. It therefore carries NO audit row and NO outbox event —
-- the saved-view/brief precedent, and the reason it is listed in
-- backend/tableownership_test.go rather than the write-shape gate. RLS binds
-- the workspace; the store binds user_id = the acting principal, because RLS
-- alone would let one rep read another rep's reading habits.

CREATE TABLE user_record_view (
  id             uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id   uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  user_id        uuid NOT NULL,
  entity_type    text NOT NULL CHECK (entity_type IN ('organization')),
  entity_id      uuid NOT NULL,
  last_viewed_at timestamptz NOT NULL,
  UNIQUE (workspace_id, user_id, entity_type, entity_id),
  -- Composite reference: a view mark can only name a user of its own workspace.
  CONSTRAINT user_record_view_user_id_fkey FOREIGN KEY (workspace_id, user_id)
    REFERENCES app_user (workspace_id, id) ON DELETE CASCADE
);

ALTER TABLE user_record_view ENABLE ROW LEVEL SECURITY;
ALTER TABLE user_record_view FORCE ROW LEVEL SECURITY;
CREATE POLICY user_record_view_tenant_isolation ON user_record_view
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

-- The company view filters signals by account on every page load. Without this
-- the filter is a sequential scan of the whole signal table per render.
CREATE INDEX signal_resolved_org_ix
  ON signal (workspace_id, resolved_org_id)
  WHERE resolved_org_id IS NOT NULL;

-- The direct-subject arm of that same filter: a signal created ABOUT an
-- organization never gets a resolved_org_id, so it is found through
-- (entity_type, entity_id) instead.
CREATE INDEX signal_entity_ix
  ON signal (workspace_id, entity_type, entity_id)
  WHERE entity_id IS NOT NULL;
