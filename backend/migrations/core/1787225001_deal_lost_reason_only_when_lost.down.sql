-- The repair UPDATE is not reversible: the cleared reasons described closes
-- that had already been reversed, and the rows carry no record of what the
-- text was. Dropping the constraint restores the old permissiveness, which is
-- all the down half can honestly do.
SET LOCAL lock_timeout = '3s';

ALTER TABLE deal
  DROP CONSTRAINT IF EXISTS deal_lost_reason_only_when_lost;
