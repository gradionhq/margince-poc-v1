-- Reverse of 20260817100000: the twelve overlay tables carry the tenant column again.
--
-- The backfill reads the LIVE workspace — archived_at IS NULL, oldest first.
-- 0217 refuses more than one live tenant and 0272 refuses to proceed while an
-- archived one still holds records, so there was exactly one workspace these
-- rows could have belonged to when the forward half ran. Ordering by created_at
-- alone would hand every restored row to whichever workspace was created first,
-- archived or not.
--
-- If no live workspace exists and a table is not empty, SET NOT NULL fails and
-- the rollback stops — the honest outcome, since no value this migration could
-- write would be true. A rollback on an empty database (the reverse-and-reapply
-- lane) has nothing to attribute and passes.
ALTER TABLE incumbent_connection ADD COLUMN workspace_id uuid;
ALTER TABLE overlay_sync_state ADD COLUMN workspace_id uuid;
ALTER TABLE overlay_mirror_halt ADD COLUMN workspace_id uuid;
ALTER TABLE overlay_mirror ADD COLUMN workspace_id uuid;
ALTER TABLE overlay_association ADD COLUMN workspace_id uuid;
ALTER TABLE overlay_tombstone ADD COLUMN workspace_id uuid;
ALTER TABLE overlay_write_ledger ADD COLUMN workspace_id uuid;
ALTER TABLE overlay_backfill_cursor ADD COLUMN workspace_id uuid;
ALTER TABLE overlay_reconcile_watermark ADD COLUMN workspace_id uuid;
ALTER TABLE mirror_user_map ADD COLUMN workspace_id uuid;
ALTER TABLE mirror_user_automap_block ADD COLUMN workspace_id uuid;
ALTER TABLE mirror_visibility ADD COLUMN workspace_id uuid;

DO $$
DECLARE
  live uuid := (SELECT id FROM workspace WHERE archived_at IS NULL ORDER BY created_at LIMIT 1);
  t    text;
BEGIN
  FOREACH t IN ARRAY ARRAY[
    'incumbent_connection', 'overlay_sync_state', 'overlay_mirror_halt',
    'overlay_mirror', 'overlay_association', 'overlay_tombstone',
    'overlay_write_ledger', 'overlay_backfill_cursor', 'overlay_reconcile_watermark',
    'mirror_user_map', 'mirror_user_automap_block', 'mirror_visibility'
  ] LOOP
    EXECUTE format('UPDATE %I SET workspace_id = $1 WHERE workspace_id IS NULL', t) USING live;
    EXECUTE format('ALTER TABLE %I ALTER COLUMN workspace_id SET NOT NULL', t);
  END LOOP;
END $$;

ALTER TABLE incumbent_connection ADD CONSTRAINT incumbent_connection_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE overlay_sync_state ADD CONSTRAINT overlay_sync_state_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE overlay_mirror_halt ADD CONSTRAINT overlay_mirror_halt_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE overlay_mirror ADD CONSTRAINT overlay_mirror_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE overlay_association ADD CONSTRAINT overlay_association_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE overlay_tombstone ADD CONSTRAINT overlay_tombstone_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE overlay_write_ledger ADD CONSTRAINT overlay_write_ledger_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE overlay_backfill_cursor ADD CONSTRAINT overlay_backfill_cursor_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE overlay_reconcile_watermark ADD CONSTRAINT overlay_reconcile_watermark_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE mirror_user_map ADD CONSTRAINT mirror_user_map_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE mirror_user_automap_block ADD CONSTRAINT mirror_user_automap_block_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE mirror_visibility ADD CONSTRAINT mirror_visibility_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;

DROP INDEX idx_mirror_user_map;
DROP INDEX idx_mirror_visibility_record;
DROP INDEX idx_overlay_association_to;

CREATE INDEX idx_mirror_user_map ON mirror_user_map (workspace_id, incumbent, incumbent_user_id);
CREATE INDEX idx_mirror_visibility_record ON mirror_visibility (workspace_id, object_class, external_id);
CREATE INDEX idx_overlay_association_to ON overlay_association (workspace_id, to_type, to_id);
