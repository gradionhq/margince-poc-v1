-- 0028: the manual-sharing verbs (crm.yaml record_share/record_unshare)
-- join the audit vocabulary — same additive move as 0018/0024/0026.
ALTER TABLE audit_log DROP CONSTRAINT audit_log_action_check;
ALTER TABLE audit_log ADD CONSTRAINT audit_log_action_check
  CHECK (action IN ('create','update','archive','merge','promote','restore','export','erase',
                    'login','assign','advance_stage','approve','reject',
                    'consent_grant','consent_withdraw','activity_relink',
                    'record_share','record_unshare'));
