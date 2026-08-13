-- Narrow the vocabulary back to 0187's set.
--
-- No pre-flight probe for existing `connect`/`disconnect` rows, for the reason
-- 0187 gives: `audit_log` carries FORCE ROW LEVEL SECURITY with deny-on-unset
-- and the migration role is neither superuser nor BYPASSRLS, so a SELECT here
-- binds no workspace and returns nothing whatever the table holds.
--
-- The re-added constraint is the guard. ADD CONSTRAINT validates every
-- existing row, so a ledger holding either verb fails this rollback and leaves
-- the schema intact. That is the correct outcome: audit_log is append-only,
-- and deleting the rows to force the rollback through would destroy the record
-- of when an installation began paying a data provider.
ALTER TABLE audit_log DROP CONSTRAINT audit_log_action_check;
ALTER TABLE audit_log ADD CONSTRAINT audit_log_action_check
  CHECK (action IN ('create','update','archive','merge','promote','restore','export','erase',
                    'assign','advance_stage','advance_phase','approve','reject',
                    'consent_grant','consent_withdraw','activity_relink',
                    'record_share','record_unshare','resolve',
                    'demote','import','import_undo',
                    'disqualify','anonymize','send_email','reset_data',
                    'password_link_issued'));
