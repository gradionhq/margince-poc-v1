-- ADR-0091 §8 phase D: the derived corpus drops its tenant column.
--
-- embedding and graph_interaction_edge are both DERIVED from records that no
-- longer carry a workspace. Every entity an embedding covers lost the column in
-- an earlier slice, and a graph edge is folded from activities that did too, so
-- the value on these two rows was a copy of a fact its source no longer states.
--
-- These were the last two tables waiting on the re-embed fan-out: a pass used
-- to be scoped per workspace, and the run tracked which ones it still owed. One
-- pass rebuilds the whole corpus now, so nothing reads the column at all.
--
-- SET LOCAL lock_timeout: each ALTER takes an ACCESS EXCLUSIVE lock, and an
-- unbounded wait would queue behind one open transaction for as long as it
-- lives — on embedding, behind any reindex in flight.
SET LOCAL lock_timeout = '3s';

-- The two edge indexes lead with the column, so they must be replaced rather
-- than left to fall with it: DROP COLUMN drops a multi-column index outright,
-- and the person and user lookups the graph is read by would go with it. Built
-- first, so no read is served without one.
CREATE INDEX idx_graph_edge_person_narrow ON graph_interaction_edge (person_id, last_at DESC);
CREATE INDEX idx_graph_edge_user_narrow   ON graph_interaction_edge (user_id, last_at DESC);
DROP INDEX idx_graph_edge_person;
DROP INDEX idx_graph_edge_user;
ALTER INDEX idx_graph_edge_person_narrow RENAME TO idx_graph_edge_person;
ALTER INDEX idx_graph_edge_user_narrow   RENAME TO idx_graph_edge_user;

-- The workspace foreign key on each table falls with the column.
ALTER TABLE embedding DROP COLUMN workspace_id;
ALTER TABLE graph_interaction_edge DROP COLUMN workspace_id;
