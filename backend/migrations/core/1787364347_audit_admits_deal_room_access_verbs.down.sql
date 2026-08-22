-- Narrowing the CHECK back would fail on any row already carrying one of the
-- two verbs this migration added, so those rows go first. The DELETE is scoped
-- to exactly those verbs, which by construction exist only because the up half
-- ran -- no audit history predating this migration is touched.
SET LOCAL lock_timeout = '3s';

DELETE FROM audit_log WHERE action IN ('invite', 'revoke');

ALTER TABLE audit_log ADD CONSTRAINT audit_log_action_check_v2
    CHECK (action IN ('create', 'update', 'archive', 'merge', 'promote', 'restore',
                      'export', 'erase', 'assign', 'advance_stage', 'advance_phase',
                      'approve', 'reject', 'consent_grant', 'consent_withdraw',
                      'activity_relink', 'record_share', 'record_unshare', 'resolve',
                      'demote', 'import', 'import_undo', 'disqualify', 'anonymize',
                      'send_email', 'reset_data', 'password_link_issued', 'connect',
                      'disconnect', 'schedule', 'reschedule', 'cancel', 'release',
                      'hold', 'expire', 'restrict', 'pin', 'accrue', 'pay', 'publish',
                      'pause', 'resume', 'close')) NOT VALID;

ALTER TABLE audit_log VALIDATE CONSTRAINT audit_log_action_check_v2;

ALTER TABLE audit_log DROP CONSTRAINT audit_log_action_check;

ALTER TABLE audit_log RENAME CONSTRAINT audit_log_action_check_v2 TO audit_log_action_check;
