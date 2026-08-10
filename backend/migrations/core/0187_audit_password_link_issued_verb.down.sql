-- Narrow the vocabulary back to 0173's set.
--
-- There is no pre-flight probe for existing `password_link_issued` rows, and
-- that is deliberate rather than an omission. `audit_log` carries FORCE ROW
-- LEVEL SECURITY with deny-on-unset, and the migration role is neither
-- superuser nor BYPASSRLS, so a `SELECT … FROM audit_log` here binds no
-- workspace and returns nothing whatever the table holds — a guard that reads
-- as a safety net while never firing is worse than none.
--
-- The real guard is the re-added constraint itself: `ADD CONSTRAINT` validates
-- every existing row, so a ledger holding this verb fails the rollback and the
-- transaction rolls back with the schema intact. `audit_log` is append-only, so
-- that refusal is the correct outcome — the verb cannot be removed while rows
-- use it, and deleting those rows to force the rollback through would destroy
-- the accountability record for an account-takeover-capable operation.
ALTER TABLE audit_log DROP CONSTRAINT audit_log_action_check;
ALTER TABLE audit_log ADD CONSTRAINT audit_log_action_check
  CHECK (action IN ('create','update','archive','merge','promote','restore','export','erase',
                    'assign','advance_stage','advance_phase','approve','reject',
                    'consent_grant','consent_withdraw','activity_relink',
                    'record_share','record_unshare','resolve',
                    'demote','import','import_undo',
                    'disqualify','anonymize','send_email','reset_data'));
