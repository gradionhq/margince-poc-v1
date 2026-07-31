-- Give back the check-in reminders 0148 archived that nothing will re-mint.
--
-- 0148 archived every outstanding generated check-in task, on the reasoning
-- that the corrected clock scan re-mints one for each record that is still
-- eligible and still quiet. That reasoning holds in exactly one condition —
-- the reminder automation still runs in that workspace — and the migration
-- never checked it. Two cases fall outside it, and in both the row is gone for
-- good, because 0148's down is a no-op:
--
--   * A workspace that PAUSED or archived no_activity_reminder /
--     check_in_cadence. timescan loads only enabled, unarchived clock
--     automations, so the scan that would re-mint never runs there. Every
--     reminder that workspace was carrying — including the ones for accounts
--     with an open deal, which the new rule agrees deserve one — was archived
--     with nothing to bring it back.
--   * A task a HUMAN had taken over: assigned to someone, or given a reminder.
--     The generated task carries neither (taskCreateEffect writes kind,
--     subject, due_at and links, and nothing else), so either column means a
--     person made this their work item. A re-mint does not restore that: it is
--     a new row without the assignee and without the date they chose.
--
-- Restored, not re-created, so the id, the audit trail and the links a person
-- may already have acted on all survive.
--
-- WHICH rows 0148 archived is decided EXACTLY, by the migration ledger.
--
-- dbmigrate runs a migration's SQL and its schema_migrations_core INSERT in
-- ONE transaction, and Postgres now() is that transaction's start time — so
-- 0148's `archived_at = now()` and its ledger row's `applied_at` default are
-- the same instant to the microsecond. A row 0148 archived carries exactly
-- that archived_at, and nothing else does.
--
-- Anything archived BEFORE 0148 is excluded by construction rather than by
-- inference: 0148 only touched `archived_at IS NULL`, so it never saw those
-- rows, and their archived_at cannot equal its instant. An absent ledger row
-- (0148 never ran on this database) makes the comparison NULL and restores
-- nothing, which is the right answer — there is nothing to give back.
--
-- Two weaker discriminators were considered and rejected. The audit log
-- ("0148 wrote no audit row, a human archive writes one") also matches
-- anything archived before 0148 by a path that did not audit, so it would
-- resurrect rows this migration has no business touching. And updated_at /
-- version cannot serve at all: the activity table carries
-- set_updated_at_bump_version(), so 0148's own UPDATE bumped both on every
-- row it touched.
UPDATE activity a
   SET archived_at = NULL
 WHERE a.kind = 'task'
   AND a.source = 'system'
   AND a.archived_at IS NOT NULL
   AND a.is_done = false
   AND (a.subject LIKE 'Check in — no activity since %'
     OR a.subject LIKE 'Time for a check-in — last touched %')
   -- 0148's own work, to the microsecond, and nothing archived before it.
   AND a.archived_at = (
         SELECT applied_at FROM schema_migrations_core WHERE version = '0148')
   AND (
         -- The automation that mints THIS task's wording is not running, so
         -- nothing will ever mint it again.
         --
         -- Paired per subject, not per workspace: the two automations write
         -- different tasks (no_activity_reminder says "Check in — no activity
         -- since …", check_in_cadence says "Time for a check-in — last touched
         -- …"), and a workspace can run one and have paused the other. Asking
         -- only whether EITHER is enabled would leave the paused one's tasks
         -- archived with nothing to bring them back — the same permanence this
         -- migration exists to undo, one automation further in.
         NOT EXISTS (
               SELECT 1 FROM automation au
                WHERE au.workspace_id = a.workspace_id
                  AND au.enabled
                  AND au.archived_at IS NULL
                  AND au.key = CASE
                        WHEN a.subject LIKE 'Check in — no activity since %'
                          THEN 'no_activity_reminder'
                        ELSE 'check_in_cadence'
                      END)
         -- Or a person had made it theirs, and a re-mint is not the same row.
         OR a.assignee_id IS NOT NULL
         OR a.remind_at IS NOT NULL);
