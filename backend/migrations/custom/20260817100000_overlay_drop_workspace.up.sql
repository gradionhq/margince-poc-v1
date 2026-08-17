-- 20260817100000: the incumbent overlay drops the tenant column (ADR-0091 §8 phase D).
--
-- Twelve tables, and the module they belong to is the one where "one row per
-- tenant" was most literally the design: an installation holds ONE incumbent
-- connection, ONE sync state, ONE halt. Those three were keyed on nothing but
-- the workspace, so phase B had nothing narrower to collapse them onto and gave
-- each a `UNIQUE ((true))` singleton index instead — the same "there is exactly
-- one row" the tenant key used to state, said without naming a tenant. Those
-- indexes already stand on their own, so this migration does not touch them.
--
-- The rest divide into the mirror itself (overlay_mirror, overlay_association,
-- overlay_tombstone), the people mapping between the two systems
-- (mirror_user_map, mirror_user_automap_block, mirror_visibility), and the
-- machinery that moves them (overlay_write_ledger, overlay_backfill_cursor,
-- overlay_reconcile_watermark).
--
-- Every identity key here already names something narrower than the tenant: an
-- (object_class, external_id) in the incumbent, an (app_user_id, incumbent)
-- pair, an association's own five-part shape.

ALTER TABLE incumbent_connection DROP CONSTRAINT incumbent_connection_workspace_id_fkey;
ALTER TABLE incumbent_connection DROP COLUMN workspace_id;

ALTER TABLE overlay_sync_state DROP CONSTRAINT overlay_sync_state_workspace_id_fkey;
ALTER TABLE overlay_sync_state DROP COLUMN workspace_id;

ALTER TABLE overlay_mirror_halt DROP CONSTRAINT overlay_mirror_halt_workspace_id_fkey;
ALTER TABLE overlay_mirror_halt DROP COLUMN workspace_id;

ALTER TABLE overlay_mirror DROP CONSTRAINT overlay_mirror_workspace_id_fkey;
ALTER TABLE overlay_mirror DROP COLUMN workspace_id;

ALTER TABLE overlay_association DROP CONSTRAINT overlay_association_workspace_id_fkey;
ALTER TABLE overlay_association DROP COLUMN workspace_id;

ALTER TABLE overlay_tombstone DROP CONSTRAINT overlay_tombstone_workspace_id_fkey;
ALTER TABLE overlay_tombstone DROP COLUMN workspace_id;

ALTER TABLE overlay_write_ledger DROP CONSTRAINT overlay_write_ledger_workspace_id_fkey;
ALTER TABLE overlay_write_ledger DROP COLUMN workspace_id;

ALTER TABLE overlay_backfill_cursor DROP CONSTRAINT overlay_backfill_cursor_workspace_id_fkey;
ALTER TABLE overlay_backfill_cursor DROP COLUMN workspace_id;

ALTER TABLE overlay_reconcile_watermark DROP CONSTRAINT overlay_reconcile_watermark_workspace_id_fkey;
ALTER TABLE overlay_reconcile_watermark DROP COLUMN workspace_id;

ALTER TABLE mirror_user_map DROP CONSTRAINT mirror_user_map_workspace_id_fkey;
ALTER TABLE mirror_user_map DROP COLUMN workspace_id;

ALTER TABLE mirror_user_automap_block DROP CONSTRAINT mirror_user_automap_block_workspace_id_fkey;
ALTER TABLE mirror_user_automap_block DROP COLUMN workspace_id;

ALTER TABLE mirror_visibility DROP CONSTRAINT mirror_visibility_workspace_id_fkey;
ALTER TABLE mirror_visibility DROP COLUMN workspace_id;

-- The three indexes that led with the column. DROP COLUMN removed each outright,
-- so they are recreated on what actually selects rows: an incumbent user, a
-- mirrored record, an association's far end.
CREATE INDEX idx_mirror_user_map ON mirror_user_map (incumbent, incumbent_user_id);
CREATE INDEX idx_mirror_visibility_record ON mirror_visibility (object_class, external_id);
CREATE INDEX idx_overlay_association_to ON overlay_association (to_type, to_id);
