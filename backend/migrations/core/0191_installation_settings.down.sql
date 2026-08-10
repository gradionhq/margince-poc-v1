-- Reverse of 0191. The `workspace` columns were never dropped, so the values
-- are still there and the setting rows are the copy — deleting them restores
-- the previous source of truth exactly.
--
-- The current values are carried back onto the columns first. While 0191 was
-- applied the settings surface wrote BOTH — the row and its column mirror — so
-- the two already agree and this is belt-and-braces rather than a guess. It
-- stays because the mirror is transitional (issue #521): once the readers move
-- and the columns go, the row is the only copy, and a down that assumed
-- otherwise would discard every change made since the move.
UPDATE workspace w SET
    name          = coalesce((SELECT value #>> '{}' FROM setting WHERE key = 'installation.name'), w.name),
    timezone      = coalesce((SELECT value #>> '{}' FROM setting WHERE key = 'installation.timezone'), w.timezone),
    base_currency = coalesce((SELECT value #>> '{}' FROM setting WHERE key = 'installation.base_currency'), w.base_currency)
 WHERE w.archived_at IS NULL;

DELETE FROM setting WHERE key IN (
  'installation.name',
  'installation.timezone',
  'installation.base_currency'
);

-- Removes the object from the five roles the up grants it to. Naming them
-- rather than every is_system row keeps a rollback off roles this migration
-- never touched; within those five a key the up wrote and a key the bootstrap
-- seed wrote are the same key, and down-then-up restores it either way.
DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);

    UPDATE role SET permissions = permissions #- '{objects,installation_settings}'
    WHERE (is_system AND key IN ('admin','ops','manager','rep','read_only')
      AND permissions->'objects' ? 'installation_settings')
      AND role.workspace_id = ws;
  END LOOP;
END $$;
