-- 0184: `password_link_issued` joins the audit verb vocabulary. On an
-- installation with no outbound-email channel an admin mints a single-use
-- set-password link for a member and delivers it out-of-band (ADR-0061
-- Amendment 1). That mint supersedes the member's outstanding unused tokens
-- and can be performed against a member who already has a password, so it is
-- an account-takeover-capable operation and its ledger row IS the control
-- that records it.
--
-- The verb is its own rather than an `update` on `user`: no app_user column
-- changes, so an `update` row would carry an empty before/after image and
-- claim a record mutation that did not happen. It is also load-bearing for
-- the event: storekit.Emit stamps the audit row id into the outbox envelope
-- and events.Envelope.Validate REJECTS a zero audit_log_id, so
-- `user.password_link_issued` cannot be emitted without this row existing.
--
-- Declared on crm.yaml's AuditLogEntry.action enum too — these rows are
-- returned by GET /audit-log like any other, so a strict client decoding that
-- response must be able to represent the verb. The DDL CHECK and the contract
-- enum therefore agree (no auditActionDBOnly asymmetry).
--
-- Effective set = this migration (the highest-numbered re-statement):
-- 0173's vocabulary plus password_link_issued.
ALTER TABLE audit_log DROP CONSTRAINT audit_log_action_check;
ALTER TABLE audit_log ADD CONSTRAINT audit_log_action_check
  CHECK (action IN ('create','update','archive','merge','promote','restore','export','erase',
                    'assign','advance_stage','advance_phase','approve','reject',
                    'consent_grant','consent_withdraw','activity_relink',
                    'record_share','record_unshare','resolve',
                    'demote','import','import_undo',
                    'disqualify','anonymize','send_email','reset_data',
                    'password_link_issued'));
