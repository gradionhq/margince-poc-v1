DROP INDEX IF EXISTS idx_overlay_write_ledger_opened_at;
DROP TABLE IF EXISTS overlay_mirror_halt;
ALTER TABLE overlay_write_ledger DROP CONSTRAINT overlay_write_ledger_pkey;
ALTER TABLE overlay_write_ledger
  ADD PRIMARY KEY (workspace_id, object_class, external_id, property);
ALTER TABLE overlay_write_ledger DROP COLUMN IF EXISTS value_canonical;
