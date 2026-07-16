-- Reverse 0075: restore the nullable entity_id and re-add `login` to the
-- action vocabulary (0053's set), so a full down leaves audit_log exactly as
-- 0053+0074 left it (TestMigrations_applyReverseReapply re-applies from zero).
ALTER TABLE audit_log ALTER COLUMN entity_id DROP NOT NULL;

ALTER TABLE audit_log DROP CONSTRAINT audit_log_action_check;
ALTER TABLE audit_log ADD CONSTRAINT audit_log_action_check
  CHECK (action IN ('create','update','archive','merge','promote','restore','export','erase',
                    'login','assign','advance_stage','approve','reject',
                    'consent_grant','consent_withdraw','activity_relink',
                    'record_share','record_unshare','resolve',
                    'demote','import','import_undo',
                    'disqualify','anonymize','send_email'));
