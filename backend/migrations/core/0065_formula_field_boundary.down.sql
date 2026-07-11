-- 0065 down — reverse of the up migration, in reverse dependency order (the
-- view reads the column, so drop the view first).
DROP VIEW organization_open_pipeline_rollup;
ALTER TABLE deal DROP COLUMN amount_minor_base;
