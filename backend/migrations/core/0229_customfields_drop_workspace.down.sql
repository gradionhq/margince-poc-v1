-- Reverse of 0229: the catalog carries the tenant column again.

ALTER TABLE custom_field ADD COLUMN workspace_id uuid;

UPDATE custom_field SET workspace_id = (SELECT id FROM workspace ORDER BY created_at LIMIT 1);

ALTER TABLE custom_field ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE custom_field ADD CONSTRAINT custom_field_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;

DROP INDEX idx_custom_field_object;
CREATE INDEX idx_custom_field_object ON custom_field (workspace_id, object, status);
