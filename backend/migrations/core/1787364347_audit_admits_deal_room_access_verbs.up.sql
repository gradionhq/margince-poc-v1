-- Admitting an outside person to a Deal Room, and taking that access back.
--
-- Neither verb reuses an existing one. `create`/`archive` would describe the
-- participant ROW, and the question an auditor asks months later is not "when
-- did this row appear" but "who let this person in, and who took it back" —
-- which is the access, not the record. `record_share`/`record_unshare` are the
-- nearest siblings and are wrong for the same reason in reverse: they grant a
-- SEAT sight of a record, while this admits somebody who holds no seat at all.
--
-- ADD ... NOT VALID then VALIDATE, then swap: the runner wraps each migration
-- file in one transaction, so a failure at any statement rolls the whole file
-- back and audit_log can never be left with no action constraint. The two-step
-- still shortens the ACCESS EXCLUSIVE hold, since NOT VALID takes that lock
-- without scanning and VALIDATE drops to SHARE UPDATE EXCLUSIVE for the pass.
SET LOCAL lock_timeout = '3s';

ALTER TABLE audit_log ADD CONSTRAINT audit_log_action_check_v3
    CHECK (action IN ('create', 'update', 'archive', 'merge', 'promote', 'restore',
                      'export', 'erase', 'assign', 'advance_stage', 'advance_phase',
                      'approve', 'reject', 'consent_grant', 'consent_withdraw',
                      'activity_relink', 'record_share', 'record_unshare', 'resolve',
                      'demote', 'import', 'import_undo', 'disqualify', 'anonymize',
                      'send_email', 'reset_data', 'password_link_issued', 'connect',
                      'disconnect', 'schedule', 'reschedule', 'cancel', 'release',
                      'hold', 'expire', 'restrict', 'pin', 'accrue', 'pay', 'publish',
                      'pause', 'resume', 'close', 'invite', 'revoke')) NOT VALID;

ALTER TABLE audit_log VALIDATE CONSTRAINT audit_log_action_check_v3;

ALTER TABLE audit_log DROP CONSTRAINT audit_log_action_check;

ALTER TABLE audit_log RENAME CONSTRAINT audit_log_action_check_v3 TO audit_log_action_check;
