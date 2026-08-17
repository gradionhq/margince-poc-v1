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

  -- The stamp never changes once written — the class OR the timestamp that
  -- says when it was earned. Leaving the timestamp mutable would let a writer
  -- keep the class and move the date, which is the same rewriting of history
  -- with an extra step: the qualifying moment is what a supervisory authority
  -- would be shown.
  IF OLD.retention_class IS NOT NULL
     AND (NEW.retention_class IS DISTINCT FROM OLD.retention_class
          OR NEW.retention_class_at IS DISTINCT FROM OLD.retention_class_at) THEN
    RAISE EXCEPTION 'activity % carries retention class % earned at %, which is stamped once and never re-derived', OLD.id, OLD.retention_class, OLD.retention_class_at
      USING ERRCODE = 'check_violation',
            CONSTRAINT = 'activity_retention_class_monotonic';
  END IF;

  -- A restriction must be substantiated at the moment it is written. The CHECK
  -- on the table can only see the row's own columns, so it can require a class
  -- and no more; the evidence lives in another table and this is the only
  -- place that can look. A restriction with nothing behind it is an assertion
  -- the controller cannot support when asked, which is the one situation this
  -- whole mechanism exists to avoid.
  IF OLD.restricted_at IS NULL AND NEW.restricted_at IS NOT NULL
     AND NOT EXISTS (SELECT 1 FROM activity_retention_evidence e
                      WHERE e.activity_id = NEW.id) THEN
    RAISE EXCEPTION 'activity % cannot be restricted with no retention evidence recording what qualified it', NEW.id
      USING ERRCODE = 'check_violation',
            CONSTRAINT = 'activity_restriction_needs_evidence';
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
    OR (deal_name IS NOT NULL
        AND decided_by IS NULL AND decided_by_name IS NULL AND reason IS NULL)),
  -- Non-empty, not merely present: '' passes IS NOT NULL and is exactly as
  -- unaccountable as a null when somebody asks who decided and why.
  CONSTRAINT are_pin_is_attributed CHECK (
    basis <> 'controller_pin'
    OR (length(btrim(decided_by_name)) > 0 AND length(btrim(reason)) > 0)),
  -- An id always carries its name. The reverse is deliberately allowed: a
  -- deleted deal leaves the frozen name behind with no id to point at.
  CONSTRAINT are_deal_name_with_id CHECK (deal_id IS NULL OR deal_name IS NOT NULL)
);

-- One derived row per (activity, deal, basis): a deal can qualify a record both
-- by being won and by carrying a sent offer, and those are two facts rather
-- than a duplicate. Controller pins are excluded — a record may be pinned,
-- released and pinned again, and each decision is its own row.
--
-- NULLS NOT DISTINCT is load-bearing. deal_id goes null when the deal is
-- deleted, and Postgres's default treats every null as distinct from every
-- other — so without this, a record whose deal is gone accepts unlimited
-- identical evidence rows, and the restricted-records list names the same
-- transaction over and over.
CREATE UNIQUE INDEX uq_activity_retention_evidence
  ON activity_retention_evidence (activity_id, deal_id, deal_name, basis)
  NULLS NOT DISTINCT
  WHERE basis <> 'controller_pin';

-- The FK columns are both ON DELETE SET NULL, so deleting a deal or a user has
-- to find the rows pointing at it. Without these it scans the whole table.
CREATE INDEX idx_are_deal ON activity_retention_evidence (deal_id) WHERE deal_id IS NOT NULL;
CREATE INDEX idx_are_decided_by ON activity_retention_evidence (decided_by) WHERE decided_by IS NOT NULL;
CREATE INDEX idx_are_activity ON activity_retention_evidence (activity_id);

-- Evidence is frozen, and that has to be enforced rather than asserted in a
-- comment. The runtime role holds UPDATE and DELETE on this table by default
-- (migration 0015), so without a guard the deal name, the basis and the
-- qualifying moment can all be rewritten — and the one audience this table
-- exists for is a supervisory authority asking the controller to substantiate
-- a retention claim. Evidence somebody can edit after the fact substantiates
-- nothing.
--
-- Two writes stay legal, and only two. A deal delete must be able to null
-- deal_id (the FK is ON DELETE SET NULL, and the frozen deal_name is what
-- answers afterwards); the same for decided_by when a user row goes. Every
-- other column is immutable, and a row is deleted only with the activity it
-- belongs to, through the CASCADE.
CREATE OR REPLACE FUNCTION activity_retention_evidence_is_frozen() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  -- A row goes only with the activity it substantiates, through the CASCADE.
  -- A direct delete is refused. The two are distinguishable because the
  -- CASCADE has already removed the parent by the time this fires, so the
  -- activity is gone exactly when the delete is legitimate.
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
     -- The reference may be CLEARED by its FK, never repointed.
     OR (NEW.deal_id IS NOT NULL AND NEW.deal_id IS DISTINCT FROM OLD.deal_id)
     OR (NEW.decided_by IS NOT NULL AND NEW.decided_by IS DISTINCT FROM OLD.decided_by) THEN
    RAISE EXCEPTION 'retention evidence % is frozen at the moment it qualified and may not be rewritten', OLD.id
      USING ERRCODE = 'check_violation',
            CONSTRAINT = 'activity_retention_evidence_frozen';
  END IF;

  RETURN NEW;
END;
$$;

CREATE TRIGGER activity_retention_evidence_is_frozen
  BEFORE UPDATE OR DELETE ON activity_retention_evidence
  FOR EACH ROW EXECUTE FUNCTION activity_retention_evidence_is_frozen();
