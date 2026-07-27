-- 0129: `advance_phase` joins the audit verb vocabulary. A project's phase
-- move is a first-class transition, not an update — it writes a history
-- row and emits project.phase_changed — so its audit entry must name the
-- transition too. Without this the write is rejected at INSERT time, which
-- is the one place a missing verb actually surfaces.
--
-- Effective set = this migration (the highest-numbered re-statement):
-- 0075's vocabulary plus advance_phase. crm.yaml's AuditLogEntry.action
-- carries the same addition; auditcoherence_test keeps the pair honest.
ALTER TABLE audit_log DROP CONSTRAINT audit_log_action_check;
ALTER TABLE audit_log ADD CONSTRAINT audit_log_action_check
  CHECK (action IN ('create','update','archive','merge','promote','restore','export','erase',
                    'assign','advance_stage','advance_phase','approve','reject',
                    'consent_grant','consent_withdraw','activity_relink',
                    'record_share','record_unshare','resolve',
                    'demote','import','import_undo',
                    'disqualify','anonymize','send_email'));
