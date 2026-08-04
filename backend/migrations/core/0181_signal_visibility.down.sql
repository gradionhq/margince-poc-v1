DROP INDEX IF EXISTS idx_signal_owner_private;
ALTER TABLE signal DROP CONSTRAINT IF EXISTS signal_owner_private_names_its_owner;
ALTER TABLE signal DROP CONSTRAINT IF EXISTS signal_owner_fkey;
ALTER TABLE signal DROP COLUMN IF EXISTS owner_id;
ALTER TABLE signal DROP COLUMN IF EXISTS visibility;
