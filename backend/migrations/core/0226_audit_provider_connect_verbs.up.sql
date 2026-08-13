-- 0226: `connect` and `disconnect` join the audit verb vocabulary.
--
-- Binding a licensed-data provider's API key seals a customer credential and
-- opens a path that spends the customer's money; cutting it destroys that
-- credential and stops all egress (ADR-0101, PI-AC-5). Both are exactly the
-- kind of act the ledger exists to record, and neither could be written: the
-- CHECK rejected the row, which rolled back the whole transaction, so
-- connecting a provider failed outright with a constraint violation reported
-- to the admin as a validation complaint about their own input.
--
-- The verbs are their own rather than `update` on provider_connection. An
-- `update` row is a record mutation with a before/after image, and neither of
-- these is primarily that: connect turns an installation-wide capability ON
-- and disconnect revokes a credential. Reading the ledger for "when did we
-- start paying this provider" must not require knowing which column changed.
--
-- Declared on crm.yaml's AuditLogEntry.action enum too. These rows come back
-- from GET /audit-log like any other, so a strict client decoding that
-- response must be able to represent the verbs — the DDL CHECK and the
-- contract enum agree, with no DB-only asymmetry.
--
-- Effective set = this migration (the highest-numbered re-statement):
-- 0187's vocabulary plus connect and disconnect.
ALTER TABLE audit_log DROP CONSTRAINT audit_log_action_check;
ALTER TABLE audit_log ADD CONSTRAINT audit_log_action_check
  CHECK (action IN ('create','update','archive','merge','promote','restore','export','erase',
                    'assign','advance_stage','advance_phase','approve','reject',
                    'consent_grant','consent_withdraw','activity_relink',
                    'record_share','record_unshare','resolve',
                    'demote','import','import_undo',
                    'disqualify','anonymize','send_email','reset_data',
                    'password_link_issued',
                    'connect','disconnect'));
