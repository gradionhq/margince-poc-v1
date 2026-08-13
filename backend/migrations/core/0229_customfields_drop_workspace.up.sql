-- 0229: the custom-field catalog drops the tenant column (ADR-0091 §8 phase D).
--
-- One table and one index. The two uniques already read (object, slug) and
-- (object, column_name) — phase B collapsed them — which is what makes the
-- catalog's own "a field with this name already exists" answer true for the
-- installation rather than for a tenant within it.

DROP INDEX idx_custom_field_object;
CREATE INDEX idx_custom_field_object ON custom_field (object, status);

ALTER TABLE custom_field DROP COLUMN workspace_id;
