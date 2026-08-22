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

-- The freeze goes back to naming only the deal pair, which is correct once the
-- project columns are gone: a trigger referencing a dropped column fails every
-- write to the table.
CREATE OR REPLACE FUNCTION activity_retention_evidence_is_frozen() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    IF EXISTS (SELECT 1 FROM activity a WHERE a.id = OLD.activity_id) THEN
      RAISE EXCEPTION 'retention evidence % is frozen and is removed only with the activity it substantiates', OLD.id
        USING ERRCODE = 'check_violation',
              CONSTRAINT = 'activity_retention_evidence_frozen';
    END IF;
    RETURN OLD;
  END IF;

  IF NEW.activity_id     IS DISTINCT FROM OLD.activity_id
     OR NEW.basis        IS DISTINCT FROM OLD.basis
     OR NEW.qualified_at IS DISTINCT FROM OLD.qualified_at
     OR NEW.deal_name    IS DISTINCT FROM OLD.deal_name
     OR NEW.decided_by_name IS DISTINCT FROM OLD.decided_by_name
     OR NEW.reason       IS DISTINCT FROM OLD.reason
     OR NEW.created_at   IS DISTINCT FROM OLD.created_at
     OR (NEW.deal_id IS NOT NULL AND NEW.deal_id IS DISTINCT FROM OLD.deal_id)
     OR (NEW.decided_by IS NOT NULL AND NEW.decided_by IS DISTINCT FROM OLD.decided_by) THEN
    RAISE EXCEPTION 'retention evidence % is frozen at the moment it qualified and may not be rewritten', OLD.id
      USING ERRCODE = 'check_violation',
            CONSTRAINT = 'activity_retention_evidence_frozen';
  END IF;

  RETURN NEW;
END;
$$;

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
