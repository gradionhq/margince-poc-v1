-- The query grammar traverses `relationship` as a join table, and every index
-- on its reference columns is partial on `kind`:
--
--   idx_rel_person_orgs        (person_id)       WHERE kind='employment' AND archived_at IS NULL
--   idx_rel_stakeholder_deals  (person_id)       WHERE kind='deal_stakeholder' AND …
--   idx_rel_person_projects    (person_id)       WHERE kind='project_stakeholder' AND …
--   idx_rel_org_people         (organization_id) WHERE kind='employment' AND …
--   idx_rel_deal_stakeholders  (deal_id)         WHERE kind='deal_stakeholder' AND …
--
-- The traversal emits NO `kind` predicate and cannot: it derives the edge from
-- the COLUMNS a join table holds and never learns what a link means, which is
-- what lets an arm added later become traversable with no code change. So
-- Postgres cannot prove any of those predicates and the hop's subquery degrades
-- to a sequential scan of `relationship`, re-executed once per candidate row of
-- the outer read. Measured on an SMB-sized corpus (60k people, 60k employment
-- edges) that is ~100s for one request, and the page limit does not bound it —
-- the hop is an inner join, so a selective hop predicate makes the planner walk
-- the whole outer table before it can fill a page.
--
-- These are the same columns, keyed on the predicate the traversal actually
-- carries. `archived_at IS NULL` is kept because the traversal always emits it
-- and it keeps the index off swept rows; `kind` is deliberately absent, for the
-- same reason the derivation has no opinion about it. `ended_at` is NOT in the
-- predicate: the traversal admits a future one (a notice period is still
-- current), so an index excluding dated rows could not answer it.
--
-- The narrower partial indexes stay. They still serve the kind-specific reads
-- the modules issue (a person's employment list, an account's contact count),
-- and the planner picks between them.

-- The build takes SHARE on `relationship`, which blocks every writer to it for
-- the duration. lock_timeout bounds only the WAIT for that lock, not the build:
-- without it, one long-open transaction holding a conflicting lock leaves this
-- migration queued forever and every write to the table queued behind it — the
-- deploy the index is meant to improve becomes the deploy that stalls the table.
-- Three seconds, matching core/0139, 0147 and 0165: take it in a quiet moment
-- or fail fast and loud so an operator retries in one.
SET LOCAL lock_timeout = '3s';

CREATE INDEX idx_rel_traverse_person ON relationship (person_id)
  WHERE archived_at IS NULL;
CREATE INDEX idx_rel_traverse_organization ON relationship (organization_id)
  WHERE archived_at IS NULL;
CREATE INDEX idx_rel_traverse_deal ON relationship (deal_id)
  WHERE archived_at IS NULL;
CREATE INDEX idx_rel_traverse_project ON relationship (project_id)
  WHERE archived_at IS NULL;
