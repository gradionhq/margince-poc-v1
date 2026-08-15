-- 0243: the scheduled-send verbs join the audit vocabulary.
--
-- A message a rep chose to send later moves through four acts the ledger has to
-- be able to record (ADR-0104/A155): it is scheduled, its moment may be moved,
-- it may be withdrawn, and it either fires or is held for a human. None could
-- be written before this migration — the CHECK rejected the row, which rolls
-- back the whole transaction, so scheduling a message would have failed
-- outright and reported a constraint violation to the rep as a complaint about
-- their own input.
--
-- The verbs are their own rather than create/update on scheduled_send, for the
-- reason 0226 gives for connect: an `update` row is a record mutation with a
-- before/after image, and these are not primarily that. `schedule` is a rep
-- committing to send something at a moment; `release` is that message becoming
-- a real send; `hold` is the system refusing to send it and handing it back.
-- Reading the ledger for "what did we agree to send, and did it go" must not
-- require knowing which column changed.
--
-- `reschedule` and `cancel` are separate from `update` and `archive` for the
-- same reason, and because they are the two acts a rep performs on a pending
-- message — the ledger answers "who moved this, and who called it off" without
-- a join to work out what an `update` touched.
--
-- Declared on crm.yaml's AuditLogEntry.action enum too. These rows come back
-- from GET /audit-log like any other, so a strict client decoding that response
-- must be able to represent the verbs — the DDL CHECK and the contract enum
-- agree, with no DB-only asymmetry.
--
-- Effective set = this migration (the highest-numbered re-statement):
-- 0226's vocabulary plus schedule, reschedule, cancel, release and hold.
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
                    'schedule','reschedule','cancel','release','hold'));
