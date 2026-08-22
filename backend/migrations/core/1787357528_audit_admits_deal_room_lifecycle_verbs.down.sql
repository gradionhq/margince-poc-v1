-- Narrowing the CHECK back would fail on any audit row already written under a
-- Deal Room lifecycle verb, so the rows go first. Deleting audit history is not
-- something a migration should do quietly, and this one does not: it removes
-- ONLY rows carrying the four verbs this migration added, which by construction
-- exist only because the up half ran.
SET LOCAL lock_timeout = '3s';

DELETE FROM audit_log WHERE action IN ('publish', 'pause', 'resume', 'close');

ALTER TABLE audit_log ADD CONSTRAINT audit_log_action_check_v1
    CHECK (action IN ('create', 'update', 'archive', 'merge', 'promote', 'restore',
                      'export', 'erase', 'assign', 'advance_stage', 'advance_phase',
                      'approve', 'reject', 'consent_grant', 'consent_withdraw',
                      'activity_relink', 'record_share', 'record_unshare', 'resolve',
                      'demote', 'import', 'import_undo', 'disqualify', 'anonymize',
                      'send_email', 'reset_data', 'password_link_issued', 'connect',
                      'disconnect', 'schedule', 'reschedule', 'cancel', 'release',
                      'hold', 'expire', 'restrict', 'pin', 'accrue', 'pay')) NOT VALID;

ALTER TABLE audit_log VALIDATE CONSTRAINT audit_log_action_check_v1;

ALTER TABLE audit_log DROP CONSTRAINT audit_log_action_check;

ALTER TABLE audit_log RENAME CONSTRAINT audit_log_action_check_v1 TO audit_log_action_check;
