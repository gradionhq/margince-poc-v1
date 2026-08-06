-- Reverse of 0190. The `workspace` columns were never dropped, so the values
-- are still there and the setting rows are the copy — deleting them restores
-- the previous source of truth exactly.
--
-- Any change an operator made through the settings surface while 0190 was
-- applied is lost by this down: it lives only in the setting row, because the
-- column stopped being written. That is stated rather than worked around — a
-- down migration that wrote values BACK onto the columns would silently
-- resurrect a base currency the forward path may have refused to change, and
-- the audit_log rows remain either way as the record of what was set.
DELETE FROM setting WHERE key IN (
  'installation.name',
  'installation.timezone',
  'installation.base_currency'
);

UPDATE role SET permissions = permissions #- '{objects,installation_settings}'
WHERE is_system AND permissions->'objects' ? 'installation_settings';
