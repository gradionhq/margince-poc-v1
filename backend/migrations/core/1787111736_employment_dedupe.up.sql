-- An employment edge is unique per (person, employer) while it is current.
--
-- `relationship` already dedupes every other kind that has a natural duplicate:
-- uq_rel_deal_person_role (0007) and uq_rel_project_stakeholder (0131). Only
-- employment had none, so the same person could be recorded as working at the
-- same company any number of times — and each extra row is counted by every
-- reader of the table, from the account's headcount to the relationship graph.
-- idx_rel_employer_people (1787044474) covers exactly this pair but is a plain
-- index, added for a query plan; it accepts duplicates.
--
-- THE PREDICATE DELIBERATELY DIVERGES FROM ITS SIBLING. uq_rel_project_stakeholder
-- keys on `archived_at IS NULL` alone, so a person may not be a stakeholder on
-- the same project twice, ever. Employment carries `ended_at IS NULL` as well,
-- which is a different rule: a person who LEFT a company may be hired by it
-- again, and that second employment is a new fact rather than a duplicate of
-- the old one. The two kinds differ because re-employment is real and
-- re-stakeholding is not; the divergence is the decision, not a copy-paste slip.

-- The repair half. CREATE UNIQUE INDEX fails outright on a database that
-- already holds duplicates, and at least one live installation does — so the
-- index cannot ship without this, or applying it bricks that deployment.
--
-- Archived, never deleted: an edge carries its own source, captured_by and
-- dates, so dropping the row would destroy provenance that nothing else holds.
-- The survivor is the current primary if there is one — never demote somebody's
-- recorded employer to resolve a duplicate — and otherwise the oldest row,
-- which is the one every earlier reader has already seen. id breaks a tie so
-- that two rows written in the same transaction still resolve deterministically.
WITH keeper AS (
  SELECT DISTINCT ON (person_id, organization_id) id
    FROM relationship
   WHERE kind = 'employment' AND ended_at IS NULL AND archived_at IS NULL
   ORDER BY person_id, organization_id, is_current_primary DESC, created_at, id
)
UPDATE relationship SET archived_at = now()
 WHERE kind = 'employment' AND ended_at IS NULL AND archived_at IS NULL
   AND id NOT IN (SELECT id FROM keeper);

CREATE UNIQUE INDEX uq_rel_employment
  ON relationship (person_id, organization_id)
  WHERE kind = 'employment' AND ended_at IS NULL AND archived_at IS NULL;
