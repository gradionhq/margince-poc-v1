-- SPDX-License-Identifier: BUSL-1.1
-- SPDX-FileCopyrightText: 2026 Gradion

-- Reverses custom, in the mirror of the order it builds: the constraints,
-- indexes and grants first, then custom's own tables, then the fork seam's
-- columns on core's workspace.
--
-- The workspace CHECKs are not dropped separately: a DROP COLUMN takes every
-- CHECK that names it, and x_overlay_iff_incumbent names both columns, so the
-- first DROP takes it.

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE overlay_write_ledger FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE overlay_tombstone FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE overlay_sync_state FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE overlay_reconcile_watermark FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE overlay_mirror_halt FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE overlay_mirror FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE overlay_backfill_cursor FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE overlay_association FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE mirror_visibility FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE mirror_user_map FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE mirror_user_automap_block FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE incumbent_connection FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE import_run FROM margince_app;

REVOKE SELECT,INSERT,DELETE,UPDATE ON TABLE import_record_map FROM margince_app;

ALTER TABLE IF EXISTS mirror_user_map DROP CONSTRAINT IF EXISTS mirror_user_map_app_user_id_fkey;

ALTER TABLE IF EXISTS mirror_user_automap_block DROP CONSTRAINT IF EXISTS mirror_user_automap_block_blocked_by_fkey;

ALTER TABLE IF EXISTS mirror_user_automap_block DROP CONSTRAINT IF EXISTS mirror_user_automap_block_app_user_id_fkey;

ALTER TABLE IF EXISTS import_record_map DROP CONSTRAINT IF EXISTS import_record_map_import_run_id_fkey;

DROP INDEX IF EXISTS overlay_sync_state_singleton;

DROP INDEX IF EXISTS overlay_mirror_halt_singleton;

DROP INDEX IF EXISTS incumbent_connection_singleton;

DROP INDEX IF EXISTS idx_overlay_sync_due;

DROP INDEX IF EXISTS idx_overlay_association_to;

DROP INDEX IF EXISTS idx_mirror_visibility_record;

DROP INDEX IF EXISTS idx_mirror_user_map;

DROP INDEX IF EXISTS idx_incumbent_connection_account;

DROP INDEX IF EXISTS idx_import_run_ws;

DROP INDEX IF EXISTS idx_import_record_map_run;

ALTER TABLE IF EXISTS import_run DROP CONSTRAINT IF EXISTS uq_import_run;

ALTER TABLE IF EXISTS overlay_write_ledger DROP CONSTRAINT IF EXISTS overlay_write_ledger_pkey;

ALTER TABLE IF EXISTS overlay_tombstone DROP CONSTRAINT IF EXISTS overlay_tombstone_pkey;

ALTER TABLE IF EXISTS overlay_reconcile_watermark DROP CONSTRAINT IF EXISTS overlay_reconcile_watermark_pkey;

ALTER TABLE IF EXISTS overlay_mirror DROP CONSTRAINT IF EXISTS overlay_mirror_pkey;

ALTER TABLE IF EXISTS overlay_backfill_cursor DROP CONSTRAINT IF EXISTS overlay_backfill_cursor_pkey;

ALTER TABLE IF EXISTS overlay_association DROP CONSTRAINT IF EXISTS overlay_association_pkey;

ALTER TABLE IF EXISTS mirror_visibility DROP CONSTRAINT IF EXISTS mirror_visibility_pkey;

ALTER TABLE IF EXISTS mirror_user_map DROP CONSTRAINT IF EXISTS mirror_user_map_app_user_id_incumbent_key;

ALTER TABLE IF EXISTS mirror_user_automap_block DROP CONSTRAINT IF EXISTS mirror_user_automap_block_pkey;

ALTER TABLE IF EXISTS incumbent_connection DROP CONSTRAINT IF EXISTS incumbent_connection_pkey;

ALTER TABLE IF EXISTS import_run DROP CONSTRAINT IF EXISTS import_run_pkey;

ALTER TABLE IF EXISTS import_record_map DROP CONSTRAINT IF EXISTS import_record_map_pkey;

DROP TABLE IF EXISTS overlay_write_ledger CASCADE;

DROP TABLE IF EXISTS overlay_tombstone CASCADE;

DROP TABLE IF EXISTS overlay_sync_state CASCADE;

DROP TABLE IF EXISTS overlay_reconcile_watermark CASCADE;

DROP TABLE IF EXISTS overlay_mirror_halt CASCADE;

DROP TABLE IF EXISTS overlay_mirror CASCADE;

DROP TABLE IF EXISTS overlay_backfill_cursor CASCADE;

DROP TABLE IF EXISTS overlay_association CASCADE;

DROP TABLE IF EXISTS mirror_visibility CASCADE;

DROP TABLE IF EXISTS mirror_user_map CASCADE;

DROP TABLE IF EXISTS mirror_user_automap_block CASCADE;

DROP TABLE IF EXISTS incumbent_connection CASCADE;

DROP TABLE IF EXISTS import_run CASCADE;

DROP TABLE IF EXISTS import_record_map CASCADE;

ALTER TABLE workspace DROP COLUMN IF EXISTS x_sor_mode;
ALTER TABLE workspace DROP COLUMN IF EXISTS x_incumbent;

