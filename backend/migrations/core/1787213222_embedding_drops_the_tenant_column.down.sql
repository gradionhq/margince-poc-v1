-- Reverse of the phase D drop on embedding and graph_interaction_edge.
--
-- The column comes back bound to the installation's OWN workspace, which is the
-- only workspace a single-organization installation has (ADR-0061). It is not a
-- reconstruction of what each row used to hold: the records these two tables
-- derive from lost their workspace in earlier slices, so there is no per-row
-- fact left to read one from. Restoring the shape is what a down migration owes
-- here; restoring a value nothing records would be inventing one.
--
-- An installation whose workspace table is empty has no corpus either — both
-- tables derive from records — so the subquery is only reached with a row to
-- find.
SET LOCAL lock_timeout = '3s';

ALTER TABLE embedding ADD COLUMN workspace_id uuid;
UPDATE embedding SET workspace_id = (SELECT id FROM workspace ORDER BY created_at LIMIT 1);
ALTER TABLE embedding
  ALTER COLUMN workspace_id SET NOT NULL,
  ADD CONSTRAINT embedding_workspace_id_fkey
    FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;

ALTER TABLE graph_interaction_edge ADD COLUMN workspace_id uuid;
UPDATE graph_interaction_edge SET workspace_id = (SELECT id FROM workspace ORDER BY created_at LIMIT 1);
ALTER TABLE graph_interaction_edge
  ALTER COLUMN workspace_id SET NOT NULL,
  ADD CONSTRAINT graph_interaction_edge_workspace_id_fkey
    FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;

CREATE INDEX idx_graph_edge_person_wide ON graph_interaction_edge (workspace_id, person_id, last_at DESC);
CREATE INDEX idx_graph_edge_user_wide   ON graph_interaction_edge (workspace_id, user_id, last_at DESC);
DROP INDEX idx_graph_edge_person;
DROP INDEX idx_graph_edge_user;
ALTER INDEX idx_graph_edge_person_wide RENAME TO idx_graph_edge_person;
ALTER INDEX idx_graph_edge_user_wide   RENAME TO idx_graph_edge_user;
