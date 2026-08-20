-- The commission verbs join the audit vocabulary.
--
-- A partner's money moves through acts the ledger has to be able to record:
-- it accrues on a won deal, and it is paid once someone approves it. Neither
-- could be written before this migration — the CHECK rejects the row, which
-- rolls back the whole transaction, so accruing would have failed outright and
-- taken the win that triggered it with it.
--
-- `accrue` and `pay` are their own verbs rather than create/update on
-- commission_entry, for the reason 0226 gives for connect: an `update` row is a
-- record mutation with a before/after image, and these are not primarily that.
-- `accrue` is a deal earning a partner money; `pay` is that money leaving.
-- Reading the ledger for "what did we owe, and did it go out" must not require
-- knowing which column changed.
--
-- `approve` and `void` are NOT added: approve is already in the vocabulary and
-- means the same thing here, and a void is recorded with `cancel`.
--
-- Declared on crm.yaml's AuditLogEntry.action enum too. These rows come back
-- from GET /audit-log like any other, so a strict client decoding that response
-- must be able to represent the verbs — the DDL CHECK and the contract enum
-- agree, with no DB-only asymmetry.
--
-- Effective set = this migration (the highest-numbered re-statement):
-- 0287's vocabulary plus accrue and pay.
SET LOCAL lock_timeout = '3s';

ALTER TABLE audit_log DROP CONSTRAINT audit_log_action_check;
ALTER TABLE audit_log ADD CONSTRAINT audit_log_action_check
  CHECK (action IN ('create','update','archive','merge','promote','restore','export','erase',
                    'assign','advance_stage','advance_phase','approve','reject',
                    'consent_grant','consent_withdraw','activity_relink',
                    'record_share','record_unshare','resolve',
                    'demote','import','import_undo',
                    'disqualify','anonymize','send_email','reset_data',
                    'password_link_issued',
                    'connect','disconnect',
                    'schedule','reschedule','cancel','release','hold',
                    'expire','restrict','pin',
                    'accrue','pay'));
