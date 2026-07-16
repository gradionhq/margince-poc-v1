-- 0075: audit_log is now RECORD MUTATIONS ONLY. The last non-entity verbs
-- (login, failed-login, bulk export) moved to system_log (0074), so:
--
-- 1. `login` leaves the action vocabulary — nothing writes it here anymore
--    (a verb that moves out of audit_log leaves its CHECK). `export` STAYS:
--    the DSR person export (sar.go) still audits an entity-bound export.
-- 2. entity_id becomes NOT NULL — every remaining audit_log row names the
--    record it mutated. entity_id was nullable ONLY for those non-entity
--    verbs; with them gone, a null entity_id would be a defect, so the
--    database rejects it by construction (P12: the audit spine is the
--    record's own history).
--
-- The action set below is 0053's vocabulary minus `login` (the effective
-- CHECK is the highest-numbered migration that re-states it — auditcoherence
-- reads it against crm.yaml, from which `login` is dropped in the same change).
ALTER TABLE audit_log DROP CONSTRAINT audit_log_action_check;
ALTER TABLE audit_log ADD CONSTRAINT audit_log_action_check
  CHECK (action IN ('create','update','archive','merge','promote','restore','export','erase',
                    'assign','advance_stage','approve','reject',
                    'consent_grant','consent_withdraw','activity_relink',
                    'record_share','record_unshare','resolve',
                    'demote','import','import_undo',
                    'disqualify','anonymize','send_email'));

ALTER TABLE audit_log ALTER COLUMN entity_id SET NOT NULL;
