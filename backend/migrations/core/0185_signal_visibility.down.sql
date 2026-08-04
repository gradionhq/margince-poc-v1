-- The columns come off cleanly. The archiving the up migration did does NOT
-- come back, and cannot: it stood down producer findings whose source nobody
-- else may read and whose reader could not be named, and once these columns are
-- gone nothing on the row says which those were. Un-archiving every derived
-- signal would resurrect the ones a human dismissed, which is a worse wrong
-- than leaving a handful the producer will write again on its next pass.
--
-- What IS restored is the visibility: anything the up migration narrowed reads
-- as workspace-visible again, which is what it was before.
DROP INDEX IF EXISTS idx_signal_owner_private;
ALTER TABLE signal DROP CONSTRAINT IF EXISTS signal_owner_private_names_its_owner;
ALTER TABLE signal DROP CONSTRAINT IF EXISTS signal_owner_fkey;
ALTER TABLE signal DROP COLUMN IF EXISTS owner_id;
ALTER TABLE signal DROP COLUMN IF EXISTS visibility;
