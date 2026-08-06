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

UPDATE role SET permissions = permissions #- '{objects,installation_settings}'
WHERE is_system AND permissions->'objects' ? 'installation_settings';
