-- 0024: consent enforcement plumbing (B-EP07.11/.12).
-- The audit action vocabulary gains the consent transitions (the §11
-- CHECK predates the consent surface, same additive move as 0018), and
-- consent_purpose gains the created_at the contract exposes.

ALTER TABLE audit_log DROP CONSTRAINT audit_log_action_check;
ALTER TABLE audit_log ADD CONSTRAINT audit_log_action_check
  CHECK (action IN ('create','update','archive','merge','promote','restore','export','erase',
                    'login','assign','advance_stage','approve','reject',
                    'consent_grant','consent_withdraw'));

ALTER TABLE consent_purpose ADD COLUMN created_at timestamptz NOT NULL DEFAULT now();
