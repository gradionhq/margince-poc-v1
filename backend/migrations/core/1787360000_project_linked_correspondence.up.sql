-- Correspondence filed under a project is business correspondence (D5).
--
-- Until now the evidence table could only say a DEAL qualified an activity,
-- so an email linked only to a project failed the Handelsbrief shield and the
-- erasure path scrubbed it like an internal note. A project is a commercial
-- engagement running for years; the correspondence on it is exactly what
-- §257 HGB and §147 AO oblige anybody to keep.
--
-- Three constraints here assumed a deal, and one of them (are_derived_names_its_deal)
-- admitted neither branch for a project row. They are restructured to ask the
-- question they were always asking: a controller pin carries its decider, and
-- any DERIVED basis carries the name of whatever qualified it — deal or project.

SET LOCAL lock_timeout = '3s';

ALTER TABLE activity_retention_evidence
    ADD COLUMN project_id uuid,
    ADD COLUMN project_name text;

ALTER TABLE activity_retention_evidence
    ADD CONSTRAINT activity_retention_evidence_project_id_fkey
        FOREIGN KEY (project_id) REFERENCES project(id) ON DELETE SET NULL;

-- The FK nulls project_id when a project is deleted, so the index that serves
-- the cascade must survive the row losing its id — mirroring idx_are_deal.
CREATE INDEX idx_are_project ON activity_retention_evidence USING btree (project_id)
    WHERE (project_id IS NOT NULL);

ALTER TABLE activity_retention_evidence
    DROP CONSTRAINT activity_retention_evidence_basis_check,
    ADD CONSTRAINT activity_retention_evidence_basis_check
        CHECK (basis IN ('deal_won', 'offer_beyond_draft', 'project_linked', 'controller_pin'));

-- The deal twin, unchanged in meaning: an id without the frozen name is
-- evidence that cannot be read back once the record is renamed or gone.
ALTER TABLE activity_retention_evidence
    ADD CONSTRAINT are_project_name_with_id
        CHECK ((project_id IS NULL) OR (project_name IS NOT NULL));

-- Restructured. The old form demanded deal_name on every non-pin row, which a
-- project row can never satisfy. What it was actually asserting is that a
-- DERIVED qualification names the record that earned it and carries no human
-- decider, while a pin carries a decider and no derived name. Both halves are
-- kept; only the "which record" half widens.
ALTER TABLE activity_retention_evidence
    DROP CONSTRAINT are_derived_names_its_deal,
    ADD CONSTRAINT are_derived_names_its_record
        CHECK ((basis = 'controller_pin'::text)
               OR ((deal_name IS NOT NULL OR project_name IS NOT NULL)
                   AND decided_by IS NULL AND decided_by_name IS NULL AND reason IS NULL));

-- Uniqueness already carries `basis`, so a project_linked row cannot collide
-- with a deal_won row on the same activity — an activity may honestly qualify
-- twice. What the old key could not distinguish is two DIFFERENT projects on
-- one activity over time: uq_activity_link_project admits one project link at
-- a time, but the evidence is frozen, so a relink leaves the first row standing
-- and the second must be able to land beside it.
DROP INDEX uq_activity_retention_evidence;
CREATE UNIQUE INDEX uq_activity_retention_evidence
    ON activity_retention_evidence
    USING btree (activity_id, deal_id, deal_name, project_id, project_name, basis)
    NULLS NOT DISTINCT
    WHERE (basis <> 'controller_pin'::text);

-- Backfill: the attribution ladder has been writing project links since it
-- shipped, and those links are unstamped. Of the two ways to be wrong only one
-- destroys data (the principle deals/retentionstamp.go already settled), so an
-- unstamped Handelsbrief sitting in the backlog is the destructive error.
--
-- Every link in the backlog was written by a matched thread, an inherited deal
-- link, a typed [KEY], or a person — statements, not guesses. There is no
-- weaker class of link here to be careful about.
--
-- NOT REVERSIBLE. The down migration drops the columns and the basis, which
-- takes the evidence with it, but the retention_class stamped onto the activity
-- rows is write-once at the database level and stays. That is deliberate:
-- over-retention is an argument to have, destruction is not.
-- uq_activity_link_project admits one project link per activity, so no
-- DISTINCT is needed to keep this one row per activity.
WITH linked AS (
  SELECT l.activity_id AS id, p.id AS project_id, p.name AS project_name
    FROM activity_link l
    JOIN project p ON p.id = l.project_id
   WHERE l.entity_type = 'project'
), stamped AS (
  UPDATE activity a
     SET retention_class = 'commercial_correspondence', retention_class_at = now()
   WHERE a.id IN (SELECT id FROM linked)
     AND a.retention_class IS NULL
)
INSERT INTO activity_retention_evidence (activity_id, basis, qualified_at, project_id, project_name)
SELECT id, 'project_linked', now(), project_id, project_name FROM linked
ON CONFLICT DO NOTHING;
