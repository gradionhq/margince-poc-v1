UPDATE role SET permissions = permissions #- '{objects,signal}' WHERE is_system;
ALTER TABLE audit_log DROP CONSTRAINT audit_log_action_check;
ALTER TABLE audit_log ADD CONSTRAINT audit_log_action_check
  CHECK (action IN ('create','update','archive','merge','promote','restore','export','erase',
                    'login','assign','advance_stage','approve','reject',
                    'consent_grant','consent_withdraw','activity_relink',
                    'record_share','record_unshare'));
DROP TABLE IF EXISTS signal_resolution;
DROP TABLE IF EXISTS signal;
