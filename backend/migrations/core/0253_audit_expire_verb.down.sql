-- Back to 0243's set. A down that runs while an expiry row exists would fail
-- the restated CHECK, which is the honest outcome: those rows were written
-- under a wider vocabulary and narrowing it does not un-write them.
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
                    'schedule','reschedule','cancel','release','hold'));
