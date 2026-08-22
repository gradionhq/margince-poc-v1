-- The Deal Room lifecycle writes four verbs the audit CHECK does not yet admit,
-- and an audit row that fails its CHECK takes the whole mutation down with it:
-- publishing a room would roll back rather than record.
--
-- None of the four reuses an existing verb, because none means the same thing.
-- `publish` puts editorial text in front of a party outside the company —
-- nothing else in this vocabulary crosses that boundary. `close` freezes a
-- room's content while buyer access deliberately continues, which `archive`
-- does not describe. `pause`/`resume` suspend reads without touching a single
-- credential; `hold`/`release` are retention verbs and `expire` is scheduling,
-- so borrowing any of them would make an access change read as a data-lifecycle
-- one in every audit query that groups by action.
--
-- Two-step, because audit_log is the largest table in the schema: ADD ... NOT
-- VALID takes a brief lock and does not scan, then VALIDATE scans without
-- blocking concurrent writes. Adding a validated CHECK in one statement would
-- hold an ACCESS EXCLUSIVE lock for the length of a full-table scan, which on a
-- busy installation stalls every mutation in the product.
SET LOCAL lock_timeout = '3s';

ALTER TABLE audit_log ADD CONSTRAINT audit_log_action_check_v2
    CHECK (action IN ('create', 'update', 'archive', 'merge', 'promote', 'restore',
                      'export', 'erase', 'assign', 'advance_stage', 'advance_phase',
                      'approve', 'reject', 'consent_grant', 'consent_withdraw',
                      'activity_relink', 'record_share', 'record_unshare', 'resolve',
                      'demote', 'import', 'import_undo', 'disqualify', 'anonymize',
                      'send_email', 'reset_data', 'password_link_issued', 'connect',
                      'disconnect', 'schedule', 'reschedule', 'cancel', 'release',
                      'hold', 'expire', 'restrict', 'pin', 'accrue', 'pay',
                      'publish', 'pause', 'resume', 'close')) NOT VALID;

ALTER TABLE audit_log VALIDATE CONSTRAINT audit_log_action_check_v2;

ALTER TABLE audit_log DROP CONSTRAINT audit_log_action_check;

ALTER TABLE audit_log RENAME CONSTRAINT audit_log_action_check_v2 TO audit_log_action_check;
