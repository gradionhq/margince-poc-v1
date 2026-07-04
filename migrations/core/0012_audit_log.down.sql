DROP TRIGGER IF EXISTS trg_audit_no_mutate ON audit_log;
DROP FUNCTION IF EXISTS audit_log_immutable();
DROP TABLE IF EXISTS audit_log;
