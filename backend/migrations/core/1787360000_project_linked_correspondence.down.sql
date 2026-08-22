-- Going back is NOT symmetric with going forward, and the asymmetry is the
-- point rather than an oversight.
--
-- The up migration stamped activities as commercial correspondence and wrote
-- the evidence saying why. This drops the evidence — the columns, the basis,
-- the index — but the `retention_class` on the activity rows STAYS. It is
-- write-once at the database level (activity_refuse_restricted_mutation), and
-- unstamping is not something a migration may do quietly: releasing a record
-- from a statutory floor needs a named person and a written reason, through
-- privacy.ReleaseFromFloor.
--
-- So an installation that runs this down migration is left holding activities
-- shielded by their class with the evidence for that class gone. That is a
-- worse state than either direction, and it is the honest one: the alternative
-- is destroying correspondence somebody may be obliged to keep. Run this only
-- on an installation where the feature was never used.
--
-- Postgres will refuse the basis narrowing outright if any project_linked row
-- exists, which is the correct outcome for exactly that reason.
SET LOCAL lock_timeout = '3s';

DROP INDEX uq_activity_retention_evidence;
CREATE UNIQUE INDEX uq_activity_retention_evidence
    ON activity_retention_evidence
    USING btree (activity_id, deal_id, deal_name, basis)
    NULLS NOT DISTINCT
    WHERE (basis <> 'controller_pin'::text);

ALTER TABLE activity_retention_evidence
    DROP CONSTRAINT are_derived_names_its_record,
    ADD CONSTRAINT are_derived_names_its_deal
        CHECK ((basis = 'controller_pin'::text)
               OR ((deal_name IS NOT NULL) AND decided_by IS NULL
                   AND decided_by_name IS NULL AND reason IS NULL));

ALTER TABLE activity_retention_evidence
    DROP CONSTRAINT are_project_name_with_id;

ALTER TABLE activity_retention_evidence
    DROP CONSTRAINT activity_retention_evidence_basis_check,
    ADD CONSTRAINT activity_retention_evidence_basis_check
        CHECK (basis IN ('deal_won', 'offer_beyond_draft', 'controller_pin'));

DROP INDEX idx_are_project;

ALTER TABLE activity_retention_evidence
    DROP CONSTRAINT activity_retention_evidence_project_id_fkey;

ALTER TABLE activity_retention_evidence
    DROP COLUMN project_name,
    DROP COLUMN project_id;
