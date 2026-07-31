-- Archive the check-in reminder tasks the clock scan minted before it knew
-- which records deserve one.
--
-- Until now activities.LastTouchBefore drew a candidate from ANY record with a
-- quiet activity link: a company with no open deal, every employee of a busy
-- account, a disqualified lead, and a record created yesterday whose imported
-- mail happens to be three weeks old all earned a "Check in — no activity
-- since …" or "Time for a check-in — last touched …" task. Those tasks are in
-- people's work queues now, and nothing in the row itself says it was minted
-- by the wrong rule.
--
-- Every such task is archived, not just the ones that would fail the new
-- eligibility test. Re-deriving "which of these were bogus" in SQL would mean
-- writing the eligibility rule a second time here, in a dialect nobody
-- maintains, that starts drifting from the Go query the day either changes.
-- Archiving the whole generated set is safe in both directions: the corrected
-- scan re-mints a reminder for every record that IS eligible and still quiet
-- on its next tick (the entity now rides the idempotency key, so the old
-- claims do not suppress it), and LastTouchBefore already ignores archived
-- activities, so an archived task can never act as a touch that moves a
-- record's staleness anchor.
--
-- Archived, never deleted: these rows carry real audit and provenance history,
-- and a human who acted on one must still be able to find it.
--
-- Scoped tightly so nothing hand-authored is caught: kind='task' AND
-- source='system' is the automation engine's own output, the two subject
-- prefixes are the exact strings handlers_clock.go generates, and a task a
-- human already completed keeps its done state rather than being archived out
-- from under a finished piece of work.
UPDATE activity
   SET archived_at = now()
 WHERE kind = 'task'
   AND source = 'system'
   AND archived_at IS NULL
   AND is_done = false
   AND (subject LIKE 'Check in — no activity since %'
     OR subject LIKE 'Time for a check-in — last touched %');
