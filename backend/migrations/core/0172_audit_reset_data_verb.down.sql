-- Forward-only: rolling back RE-STATES the constraint WITH reset_data rather
-- than narrowing it out. audit_log is append-only, so once a reset has written
-- a `reset_data` row, a narrowed CHECK could never be added back — the existing
-- row would violate it — and deleting the row to make the rollback succeed
-- would break the append-only ledger. Re-adding an already-present verb is a
-- harmless no-op; dropping it is not always possible, so it is never attempted.
ALTER TABLE audit_log DROP CONSTRAINT audit_log_action_check;
ALTER TABLE audit_log ADD CONSTRAINT audit_log_action_check
  CHECK (action IN ('create','update','archive','merge','promote','restore','export','erase',
                    'assign','advance_stage','advance_phase','approve','reject',
                    'consent_grant','consent_withdraw','activity_relink',
                    'record_share','record_unshare','resolve',
                    'demote','import','import_undo',
                    'disqualify','anonymize','send_email','reset_data'));
