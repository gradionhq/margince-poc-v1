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
-- ADD ... NOT VALID then VALIDATE, rather than one validated ADD.
--
-- Stated honestly, because the usual reason does NOT apply here: the runner
-- wraps each migration file in a single transaction (dbmigrate.go), so the locks
-- this takes are held until the file commits either way, and the two-step buys
-- no concurrency it would buy in a hand-run psql session. What it does buy is a
-- shorter ACCESS EXCLUSIVE hold: NOT VALID takes that lock without scanning, and
-- VALIDATE downgrades to SHARE UPDATE EXCLUSIVE for the scan, so readers and
-- writers of audit_log are blocked for a moment rather than for a full pass over
-- the largest table in the schema.
--
-- The transaction is also what makes this safe to interrupt: a failure at any
-- statement rolls the whole file back, so audit_log can never be left with no
-- action constraint at all.
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
