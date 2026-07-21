DROP TABLE IF EXISTS overlay_tombstone, overlay_write_ledger, mirror_visibility, mirror_user_map,
  overlay_association, overlay_mirror, incumbent_connection CASCADE;
ALTER TABLE workspace DROP CONSTRAINT IF EXISTS x_overlay_iff_incumbent;
ALTER TABLE workspace DROP COLUMN IF EXISTS x_incumbent;
ALTER TABLE workspace DROP COLUMN IF EXISTS x_sor_mode;
