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
-- WHICH rows 0148 archived is decided by the audit log, not by a timestamp:
-- 0148 is raw SQL and wrote no audit row, while every human archive goes
-- through the store and writes one. So an archived generated task with no
-- 'archive' entry against it is one this migration is entitled to give back,
-- and a task a human archived deliberately stays archived.
--
-- Deliberately NOT used as a discriminator: updated_at and version. The
-- activity table carries set_updated_at_bump_version(), so 0148's own UPDATE
-- bumped both on every row it touched — they no longer distinguish anything.
UPDATE activity a
   SET archived_at = NULL
 WHERE a.kind = 'task'
   AND a.source = 'system'
   AND a.archived_at IS NOT NULL
   AND a.is_done = false
   AND (a.subject LIKE 'Check in — no activity since %'
     OR a.subject LIKE 'Time for a check-in — last touched %')
   -- 0148's work, not a person's considered decision to archive it.
   AND NOT EXISTS (
         SELECT 1 FROM audit_log l
          WHERE l.entity_type = 'activity'
            AND l.entity_id = a.id
            AND l.action = 'archive')
   AND (
         -- Nothing in this workspace will ever re-mint it.
         NOT EXISTS (
               SELECT 1 FROM automation au
                WHERE au.workspace_id = a.workspace_id
                  AND au.key IN ('no_activity_reminder', 'check_in_cadence')
                  AND au.enabled
                  AND au.archived_at IS NULL)
         -- Or a person had made it theirs, and a re-mint is not the same row.
         OR a.assignee_id IS NOT NULL
         OR a.remind_at IS NOT NULL);
