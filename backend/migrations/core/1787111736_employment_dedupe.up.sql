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

-- WRITERS OUT FIRST, for the whole migration. The repair below and the index
-- at the bottom are two statements, and dbmigrate's advisory lock coordinates
-- only other MIGRATION runners: an application replica still serving traffic
-- holds ROW EXCLUSIVE, which the repair's UPDATE does not conflict with. It
-- could therefore insert a fresh duplicate for a pair the repair had just
-- cleared, and CREATE UNIQUE INDEX would abort on a row that did not exist
-- when the migration started. That is the ordinary rolling deploy — the new
-- container migrates at boot while the old one is still up.
--
-- SHARE ROW EXCLUSIVE, not EXCLUSIVE: it blocks INSERT/UPDATE/DELETE and lets
-- readers through, which is the smallest lock that closes the window.
--
-- lock_timeout for the reason 0139 sets it, and it matters MORE here than in a
-- plain drop: a pending strong lock request queues behind whatever transaction
-- is already running, and every write arriving after it queues behind the
-- request. So an unbounded wait does not merely delay this migration — it stops
-- every employment write in the installation for as long as it waits, during
-- the rolling deploy the lock above exists to survive. Three seconds turns that
-- into a fast, loud failure: the transaction rolls back whole, nothing is
-- half-applied, and the deploy retries.
--
-- ADDED AFTER THIS MIGRATION SHIPPED, which the additive-only rule normally
-- forbids. It is safe here and only here: lock_timeout governs how the lock is
-- ACQUIRED and touches neither schema nor data, so a database that already
-- applied this version is unaffected and a fresh one lands on identical rows.
-- The divergence the rule protects against cannot occur.
SET LOCAL lock_timeout = '3s';
LOCK TABLE relationship IN SHARE ROW EXCLUSIVE MODE;

-- An employment somebody has LEFT is not their CURRENT primary one, and until
-- this migration nothing enforced that: UpdateRelationship carried the flag
-- through an `ended_at` patch untouched, so a deployed database holds rows that
-- are ended and still flagged. They are not cosmetic. Every reader of the
-- column filters on the flag ALONE — the person-by-employer list, the account
-- contact count, enrichment's employer lookup and the auto-enrich sweep — so
-- such a row still counts its person at a company they left.
--
-- It also has to run BEFORE the promotion at the bottom, which cannot promote
-- past a live primary flag without violating uq_rel_current_primary_employer.
-- Leaving these rows would therefore leave the person the report is about with
-- an employer they left and no current one, which is the defect wearing a
-- different face.
UPDATE relationship SET is_current_primary = false
 WHERE kind = 'employment' AND is_current_primary
   AND ended_at IS NOT NULL AND archived_at IS NULL;

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

-- The second half of the repair, and the same rule the store now applies to
-- every write: a person whose ONE current employment carries no primary flag
-- has it promoted. Without this the defect is fixed for everything written from
-- here and left standing in exactly the rows that produced the report.
--
-- Only where the answer is unambiguous: the NOT EXISTS excludes a person who has
-- another current employment, because which of two employers is primary is a
-- question nothing here can answer. It runs after the dedupe above so an
-- archived duplicate no longer counts as a second current employment.
--
-- Its `OR o.is_current_primary` arm reads as dead now that the step at the top
-- has cleared every ended-and-flagged row, and it is not: it is the same
-- predicate the store's insert uses, and it is what keeps this statement from
-- ever violating uq_rel_current_primary_employer if a flagged row survives for
-- a reason this migration did not anticipate.
UPDATE relationship r SET is_current_primary = true
 WHERE r.kind = 'employment' AND r.ended_at IS NULL AND r.archived_at IS NULL
   AND NOT r.is_current_primary
   AND NOT EXISTS (
     SELECT 1 FROM relationship o
      WHERE o.kind = 'employment' AND o.person_id = r.person_id
        AND o.archived_at IS NULL AND o.id <> r.id
        AND (o.ended_at IS NULL OR o.is_current_primary));

CREATE UNIQUE INDEX uq_rel_employment
  ON relationship (person_id, organization_id)
  WHERE kind = 'employment' AND ended_at IS NULL AND archived_at IS NULL;
