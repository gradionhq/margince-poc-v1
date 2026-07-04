-- The approval inbox's decisions are first-class audited facts, but the
-- data-model §11 action vocabulary predates it (spec defect: fable
-- feedback 17) — 'approve'/'reject' extend the CHECK additively.
ALTER TABLE audit_log DROP CONSTRAINT audit_log_action_check;
ALTER TABLE audit_log ADD CONSTRAINT audit_log_action_check
  CHECK (action IN ('create','update','archive','merge','promote','restore','export','erase','login','assign','advance_stage','approve','reject'));
