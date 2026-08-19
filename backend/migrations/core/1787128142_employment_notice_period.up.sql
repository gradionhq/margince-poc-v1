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
-- ROW-LEVEL PROVENANCE, because "restore what was cleared" and "flag every sole
-- notice-period employment" are different sets and only the first is this
-- migration's business. 1787111736 ran as ONE transaction, so every row its
-- UPDATE touched carries the same `updated_at` — the trigger stamps it — and
-- that timestamp is at or before the moment the ledger recorded the version.
-- A row somebody created afterwards with `is_current_primary = false` on
-- purpose has a LATER `updated_at`, so bounding on the ledger excludes it
-- rather than reversing a decision its author can see themselves having made.
--
-- coalesce to the epoch when the ledger has no row for that version: on a fresh
-- database this migration runs in the same deploy as 1787111736 and there is
-- nothing to repair, so an empty bound must select nothing rather than
-- everything.
UPDATE relationship r SET is_current_primary = true
 WHERE r.kind = 'employment' AND r.archived_at IS NULL
   AND NOT r.is_current_primary
   AND r.ended_at > current_date
   AND r.updated_at <= coalesce(
         (SELECT applied_at FROM schema_migrations_core WHERE version = '1787111736'),
         '-infinity'::timestamptz)
   AND NOT EXISTS (
     SELECT 1 FROM relationship o
      WHERE o.kind = 'employment' AND o.person_id = r.person_id
        AND o.archived_at IS NULL AND o.id <> r.id
        AND (o.is_current_primary
             OR o.ended_at IS NULL
             OR o.ended_at > current_date));
