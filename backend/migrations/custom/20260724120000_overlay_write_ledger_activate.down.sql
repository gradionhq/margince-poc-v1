DROP INDEX IF EXISTS idx_overlay_write_ledger_opened_at;
DROP TABLE IF EXISTS overlay_mirror_halt;
ALTER TABLE overlay_write_ledger DROP COLUMN IF EXISTS value_canonical;
