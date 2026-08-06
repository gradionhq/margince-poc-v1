-- Deliberately a no-op, for the same reason as core 0190's down: this migration
-- writes nothing 20260730130000 does not already declare, so reverting it must
-- leave the import_run grants standing. That migration's own down removes them.
SELECT 1;
