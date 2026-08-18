-- Back to 0290's function, where a lift could leave the subject's address behind.
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

  -- The stamp never changes once written — the class OR the timestamp saying
  -- when it was earned. Leaving the timestamp mutable would let a writer keep
  -- the class and move the date: the same rewriting of history with an extra
  -- step, on the field a supervisory authority would be shown.
  IF OLD.retention_class IS NOT NULL
     AND (NEW.retention_class IS DISTINCT FROM OLD.retention_class
          OR NEW.retention_class_at IS DISTINCT FROM OLD.retention_class_at) THEN
    RAISE EXCEPTION 'activity % carries retention class % earned at %, which is stamped once and never re-derived', OLD.id, OLD.retention_class, OLD.retention_class_at
      USING ERRCODE = 'check_violation',
            CONSTRAINT = 'activity_retention_class_monotonic';
  END IF;

  -- A restriction is substantiated at the moment it is written. The table
  -- CHECK sees only this row, so it can require a class and no more; the
  -- evidence is in another table and this is the only place that can look.
  IF OLD.restricted_at IS NULL AND NEW.restricted_at IS NOT NULL
     AND NOT EXISTS (SELECT 1 FROM activity_retention_evidence e
                      WHERE e.activity_id = NEW.id) THEN
    RAISE EXCEPTION 'activity % cannot be restricted with no retention evidence recording what qualified it', NEW.id
      USING ERRCODE = 'check_violation',
            CONSTRAINT = 'activity_restriction_needs_evidence';
  END IF;

  -- A deadline already recorded never moves nearer. A pin or a re-restriction
  -- of a row that still carries its class may only extend it.
  IF OLD.restricted_until IS NOT NULL AND NEW.restricted_at IS NOT NULL
     AND NEW.restricted_until < OLD.restricted_until THEN
    RAISE EXCEPTION 'activity % is held until % and a statutory deadline never shortens', OLD.id, OLD.restricted_until
      USING ERRCODE = 'check_violation',
            CONSTRAINT = 'activity_restriction_never_shortens';
  END IF;

  IF OLD.restricted_at IS NOT NULL THEN
    -- Still restricted after the write: refused outright.
    IF NEW.restricted_at IS NOT NULL THEN
      RAISE EXCEPTION 'activity % is restricted under a statutory retention obligation until %', OLD.id, OLD.restricted_until
        USING ERRCODE = 'check_violation',
              CONSTRAINT = 'activity_restricted_immutable';
    END IF;

    -- Lifting: the content goes with it, in this statement. Both legitimate
    -- callers erase as they lift, and a lift that leaves the body readable is
    -- not a completion of the erasure — it is a way to undo the restriction
    -- and keep the data.
    IF NEW.body IS NOT NULL OR NEW.subject IS NOT NULL OR NEW.raw IS NOT NULL THEN
      RAISE EXCEPTION 'activity % may only leave restriction by being erased: clear body, subject and raw in the same statement', OLD.id
        USING ERRCODE = 'check_violation',
              CONSTRAINT = 'activity_restriction_lift_erases';
    END IF;
  END IF;

  RETURN NEW;
END;
$$;
