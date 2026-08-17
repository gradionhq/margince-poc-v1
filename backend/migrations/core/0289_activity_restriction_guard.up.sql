-- 0289: the class stamp is write-once, the restriction is immutable, and what
-- qualified a record outlives what it points at (A165/ADR-0114, A167/ADR-0116).
--
-- DEPACK-AC-5a puts the immutability below every role, admin included, so it
-- cannot live in a handler. Two rules, one trigger.
--
-- THE STAMP IS WRITE-ONCE. A165 makes the classification monotonic precisely
-- because qualification is reversible in the product, and a column nothing
-- protects is monotonic only by the good behaviour of every writer that ever
-- touches it — which is the assumption A165 already rejected once. Nothing
-- legitimate needs to unset it: a record wrongly classified is handled by the
-- audited release, which erases it rather than rewriting history about it.
--
-- THE RESTRICTION IS IMMUTABLE. Any write to a restricted row is refused except
-- one that CLEARS restricted_at, which is the shape the expiry sweep and the
-- audited release both take, and nothing else.
--
-- Two details the 0193 precedent does not carry, both silent failures:
--
--   RETURN OLD on a permitted DELETE. NEW is null on DELETE, so returning it
--   would block every delete of every row rather than only restricted ones.
--
--   The retention engine's ordinary selectors must EXCLUDE restricted rows.
--   Otherwise the nightly pass attempts a write this trigger refuses, the
--   policy errors, and one stuck row stops every later scope every night — a
--   compliance engine that has silently stopped running looks identical to one
--   with nothing to do.
CREATE OR REPLACE FUNCTION activity_refuse_restricted_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    IF OLD.restricted_at IS NOT NULL THEN
      RAISE EXCEPTION 'activity % is restricted under a statutory retention obligation until %', OLD.id, OLD.restricted_until
        USING ERRCODE = 'check_violation',
              CONSTRAINT = 'activity_restricted_immutable';
    END IF;
    RETURN OLD;
  END IF;

  -- The stamp never changes once written. Clearing it or moving it to another
  -- class both count.
  IF OLD.retention_class IS NOT NULL
     AND (NEW.retention_class IS DISTINCT FROM OLD.retention_class) THEN
    RAISE EXCEPTION 'activity % carries retention class %, which is stamped once and never re-derived', OLD.id, OLD.retention_class
      USING ERRCODE = 'check_violation',
            CONSTRAINT = 'activity_retention_class_monotonic';
  END IF;

  -- A restricted row admits exactly one write: the one that lifts the
  -- restriction. The expiry sweep and the audited release are both that shape.
  IF OLD.restricted_at IS NOT NULL AND NEW.restricted_at IS NOT NULL THEN
    RAISE EXCEPTION 'activity % is restricted under a statutory retention obligation until %', OLD.id, OLD.restricted_until
      USING ERRCODE = 'check_violation',
            CONSTRAINT = 'activity_restricted_immutable';
  END IF;

  RETURN NEW;
END;
$$;

CREATE TRIGGER activity_refuse_restricted_mutation
  BEFORE UPDATE OR DELETE ON activity
  FOR EACH ROW EXECUTE FUNCTION activity_refuse_restricted_mutation();

-- What qualified a record for the floor, frozen at the moment it qualified.
--
-- This is its own table because the evidence does NOT survive in the place a
-- reader would look for it. The obvious implementation joins activity_link to
-- deal at read time, and that answers wrongly and SILENTLY: activity_link
-- CASCADEs on deal delete, a relink deletes the link it replaces, a won deal
-- can be reopened, and a deal can be renamed. A controller asking "why is this
-- record held?" six months later would get an empty answer about a record that
-- is genuinely and correctly held — in front of the one audience this exists
-- for, a supervisory authority asking them to substantiate a retention claim.
CREATE TABLE activity_retention_evidence (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  activity_id   uuid NOT NULL REFERENCES activity(id) ON DELETE CASCADE,
  basis         text NOT NULL CHECK (basis IN ('deal_won','offer_beyond_draft','controller_pin')),
  qualified_at  timestamptz NOT NULL,

  -- deal_id is the live row while it exists; deal_name is frozen so the
  -- evidence still answers after a rename or a delete. SET NULL rather than
  -- CASCADE: a cascading FK would delete the proof along with the thing it
  -- proves. A controller pin may name no deal at all — supplier and purchasing
  -- correspondence qualifies under §257 HGB and has no deal in this product to
  -- hang off (DEPACK-AC-5h), and that case is why pin exists.
  deal_id       uuid NULL REFERENCES deal(id) ON DELETE SET NULL,
  deal_name     text NULL,

  -- A pin is a finding of fact by a named accountable person (Art. 5(2)). The
  -- display name is frozen beside the id so a deactivated or deleted account
  -- cannot turn an attributed decision into an anonymous one.
  decided_by      uuid NULL REFERENCES app_user(id) ON DELETE SET NULL,
  decided_by_name text NULL,
  reason          text NULL,
  created_at    timestamptz NOT NULL DEFAULT now(),

  -- A derived basis IS the transaction, so it names one and carries no
  -- controller reason. It requires the NAME, not the id: deal_id is
  -- ON DELETE SET NULL, so demanding it would make deleting a deal fail
  -- against its own evidence — and the evidence outliving the deal is the
  -- whole reason this table exists. deal_name is frozen at qualification and
  -- is what still answers afterwards.
  CONSTRAINT are_derived_names_its_deal CHECK (
    basis = 'controller_pin'
    OR (deal_name IS NOT NULL AND decided_by IS NULL AND reason IS NULL)),
  CONSTRAINT are_pin_is_attributed CHECK (
    basis <> 'controller_pin'
    OR (decided_by_name IS NOT NULL AND reason IS NOT NULL)),
  -- An id always carries its name. The reverse is deliberately allowed: a
  -- deleted deal leaves the frozen name behind with no id to point at.
  CONSTRAINT are_deal_name_with_id CHECK (deal_id IS NULL OR deal_name IS NOT NULL)
);

-- One derived row per (activity, deal, basis): a deal can qualify a record both
-- by being won and by carrying a sent offer, and those are two facts rather
-- than a duplicate. Controller pins are excluded — a record may be pinned,
-- released and pinned again, and each decision is its own row.
CREATE UNIQUE INDEX uq_activity_retention_evidence
  ON activity_retention_evidence (activity_id, deal_id, basis)
  WHERE basis <> 'controller_pin';
CREATE INDEX idx_are_activity ON activity_retention_evidence (activity_id);
