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
-- loss, it does not adjudicate between two employers (#1781). The uniqueness
-- of the result is what uq_rel_current_primary_employer would otherwise refuse,
-- so the NOT EXISTS is load-bearing, not defensive.
--
-- `ended_at > current_date` and nothing else: a row with no end date was never
-- touched by 1787111736, and one already past is correctly unflagged.
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
