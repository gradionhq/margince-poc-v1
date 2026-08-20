-- Reverses 1787228559: back to 0287's vocabulary.
--
-- Rows already written with the removed verbs are left alone. The CHECK binds
-- new writes; deleting an installation's audit history to satisfy a rollback
-- would destroy the record the ledger exists to keep.
SET LOCAL lock_timeout = '3s';

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
                    'expire','restrict','pin'))
  NOT VALID;
