-- A clean rollback or an honest failure — never schema-versus-ledger drift.
-- audit_log is append-only, so once a reset has written a `reset_data` row the
-- verb cannot be removed from the CHECK without the existing row violating it
-- (and deleting the row to force the rollback through would break the ledger).
-- So: if any `reset_data` row exists, refuse the rollback before touching the
-- constraint; otherwise narrow the vocabulary back to 0133's set.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM audit_log WHERE action = 'reset_data') THEN
    RAISE EXCEPTION 'cannot roll back 0172: audit_log holds reset_data rows and is append-only — the verb cannot be removed from audit_log_action_check';
  END IF;
END $$;

ALTER TABLE audit_log DROP CONSTRAINT audit_log_action_check;
ALTER TABLE audit_log ADD CONSTRAINT audit_log_action_check
  CHECK (action IN ('create','update','archive','merge','promote','restore','export','erase',
                    'assign','advance_stage','advance_phase','approve','reject',
                    'consent_grant','consent_withdraw','activity_relink',
                    'record_share','record_unshare','resolve',
                    'demote','import','import_undo',
                    'disqualify','anonymize','send_email'));
