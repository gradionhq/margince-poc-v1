ALTER TABLE organization DROP COLUMN IF EXISTS quarantined_at;
ALTER TABLE organization DROP COLUMN IF EXISTS visibility;
ALTER TABLE person DROP COLUMN IF EXISTS quarantined_at;
ALTER TABLE person DROP COLUMN IF EXISTS visibility;
