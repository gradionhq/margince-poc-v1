-- A clean rollback or an honest failure — never schema-versus-ledger drift.
-- audit_log is append-only, so once an admin has issued a set-password link
-- the verb cannot be removed from the CHECK without the existing row
-- violating it (and deleting the row to force the rollback through would
-- break the ledger — worse here than elsewhere, because that row is the
-- accountability record for an account-takeover-capable operation).
-- So: if any `password_link_issued` row exists, refuse the rollback before
-- touching the constraint; otherwise narrow the vocabulary back to 0173's set.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM audit_log WHERE action = 'password_link_issued') THEN
    RAISE EXCEPTION 'cannot roll back 0184: audit_log holds password_link_issued rows and is append-only — the verb cannot be removed from audit_log_action_check';
  END IF;
END $$;

ALTER TABLE audit_log DROP CONSTRAINT audit_log_action_check;
ALTER TABLE audit_log ADD CONSTRAINT audit_log_action_check
  CHECK (action IN ('create','update','archive','merge','promote','restore','export','erase',
                    'assign','advance_stage','advance_phase','approve','reject',
                    'consent_grant','consent_withdraw','activity_relink',
                    'record_share','record_unshare','resolve',
                    'demote','import','import_undo',
                    'disqualify','anonymize','send_email','reset_data'));
