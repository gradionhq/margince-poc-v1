-- 0171: `reset_data` joins the audit verb vocabulary. The non-production
-- admin "reset data" endpoint (a dev-only sweep + reseed of the bound
-- workspace) records itself as a workspace-entity audit row through the
-- same storekit.AuditWithEvidence write shape every mutation uses — without
-- this the INSERT is rejected at write time, which is the one place a
-- missing verb actually surfaces.
--
-- reset_data is an internal admin-tooling action rather than a CRM domain
-- mutation, so it is a DB-only verb (see auditcoherence_test.go's
-- auditActionDBOnly waiver) rather than an addition to crm.yaml's
-- AuditLogEntry.action enum.
--
-- Effective set = this migration (the highest-numbered re-statement):
-- 0133's vocabulary plus reset_data.
ALTER TABLE audit_log DROP CONSTRAINT audit_log_action_check;
ALTER TABLE audit_log ADD CONSTRAINT audit_log_action_check
  CHECK (action IN ('create','update','archive','merge','promote','restore','export','erase',
                    'assign','advance_stage','advance_phase','approve','reject',
                    'consent_grant','consent_withdraw','activity_relink',
                    'record_share','record_unshare','resolve',
                    'demote','import','import_undo',
                    'disqualify','anonymize','send_email','reset_data'));
