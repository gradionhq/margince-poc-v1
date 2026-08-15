-- 0251: expiry joins the audit vocabulary.
--
-- An approval nobody answers auto-rejects when its window closes
-- (APPR-PARAM-1: "unactioned means rejected"), and APPR-AC-2 says that outcome
-- is "logged like any other decision, attributed to a system actor". Until now
-- it was logged like no decision at all: expiry was computed at read time and
-- written nowhere, so an item that refused itself left no trace of having done
-- so.
--
-- The sweep that records it (approvals/expiresweep.go) writes an audit row, and
-- without this migration the CHECK rejects that row — which rolls back the whole
-- transaction, so the expiry would fail outright and the staging would stay
-- pending forever. The gate would have looked like a bug in the sweep rather
-- than a missing verb.
--
-- Its own verb rather than `reject`, and the distinction is the point a reader
-- of the ledger needs: `reject` is a person declining something, `expire` is
-- nobody deciding at all. They have the same effect on the staged action and
-- very different things to say about the team — a column of expiries is work
-- going unanswered, which no count of rejections would show.
--
-- Declared on crm.yaml's AuditLogEntry.action enum too, for the reason 0243
-- gives: these rows come back from GET /audit-log, so a strict client decoding
-- that response must be able to represent the verb. DDL and contract agree.
--
-- Effective set = this migration (the highest-numbered re-statement):
-- 0243's vocabulary plus expire.
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
                    'expire'));
