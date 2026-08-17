-- 0287: restriction and pinning join the audit vocabulary.
--
-- A165/ADR-0114 makes a Handelsbrief survive an Art. 17 erasure in restricted
-- form rather than being destroyed, and A167/ADR-0116 pins the four verbs that
-- outcome writes. Two of them already exist here: `release` (0243) and `expire`
-- (0253). Two do not, and without them the feature cannot write its audit row
-- at all — the CHECK rejects it, which rolls back the transaction the
-- restriction commits in, so the erasure fails outright rather than logging
-- badly. That would read as a bug in the erasure path rather than a missing
-- verb, which is exactly how 0253 describes the same failure for `expire`.
--
-- Their meanings, so a reader of the ledger is not left inferring them from the
-- word (ADR-0116 §6):
--
--   restrict — an erasure shielded a record under the statutory floor instead
--              of destroying it. The record survives in storage and leaves
--              every ordinary read path.
--   pin      — an administrator placed a record under the floor that the
--              derivation could not see, with a stated reason. Supplier and
--              purchasing correspondence is the named case: it qualifies under
--              §257 HGB and has no deal in this product to hang off.
--
-- `restrict` is not `archive`, and the distinction is what a supervisory
-- authority is asking about. An archive is the business tidying its own view
-- and is reversible by the business. A restriction is an obligation the
-- business is under, with a deadline it did not choose, holding a record it
-- would otherwise have been required to erase.
--
-- Declared on crm.yaml's AuditLogEntry.action enum too, for the reason 0243
-- gives: these rows come back from GET /audit-log, so a strict client decoding
-- that response must be able to represent the verb. DDL and contract agree.
--
-- Effective set = this migration (the highest-numbered re-statement):
-- 0253's vocabulary plus restrict and pin.
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
                    'expire','restrict','pin'));
