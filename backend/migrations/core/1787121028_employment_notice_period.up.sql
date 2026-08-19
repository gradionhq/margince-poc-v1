-- Restore the current-primary flag that 1787111736 stripped from people serving
-- a notice period.
--
-- That migration cleared `is_current_primary` on every live employment with a
-- non-null `ended_at`, meaning to clear it on jobs somebody had LEFT. A future
-- `ended_at` is not a job somebody has left — it is a last day that has been
-- recorded and has not arrived. Anyone whose notice was on file when
-- 1787111736 applied lost their employer from the account's contact count, from
-- the person-by-employer list and from their own 360, while still working there.
--
-- This is additive repair, not an edit to history: 1787111736 has applied on
-- deployed databases and will not re-run there, so the correction has to reach
-- them as its own version. A fresh installation runs both in order and lands in
-- the same place.

SET LOCAL lock_timeout = '3s';
LOCK TABLE relationship IN SHARE ROW EXCLUSIVE MODE;

-- Only where the answer is unambiguous, and only where the flag is FREE to
-- give: a person who already holds a live primary keeps it — this repairs a
-- loss, it does not adjudicate between two employers (#1781). The uniqueness of
-- the result is what uq_rel_current_primary_employer would otherwise refuse, so
-- the NOT EXISTS is load-bearing rather than defensive.
--
-- `ended_at > current_date`, matching people.EmploymentIsCurrentSQL exactly: a
-- last day that has ARRIVED is a departure, so only a future one is a notice
-- period. A row with no end date was never touched, and one already past is
-- correctly unflagged.
--
-- WHAT THIS CANNOT DISTINGUISH, stated rather than glossed: a row the old
-- predicate cleared and a row somebody deliberately created non-primary look
-- identical afterwards — nothing recorded which happened. So a notice-period
-- employment that was chosen to be non-primary, is somebody's only one, and was
-- created in the window between 1787111736's deploy and this one, is promoted
-- here against its author's intent. That window is hours wide and the outcome is
-- one PATCH away; leaving every genuinely damaged row unrepaired to avoid it
-- would be the worse trade. The alternative — reading `version` to guess which
-- rows the migration touched — would be a guess dressed as a fact.
UPDATE relationship r SET is_current_primary = true
 WHERE r.kind = 'employment' AND r.archived_at IS NULL
   AND NOT r.is_current_primary
   AND r.ended_at > current_date
   AND NOT EXISTS (
     SELECT 1 FROM relationship o
      WHERE o.kind = 'employment' AND o.person_id = r.person_id
        AND o.archived_at IS NULL AND o.id <> r.id
        AND (o.is_current_primary
             OR o.ended_at IS NULL
             OR o.ended_at > current_date));
