ALTER TABLE offer DROP CONSTRAINT IF EXISTS offer_template_id_fkey;
ALTER TABLE offer DROP COLUMN IF EXISTS template_id;
DROP TABLE IF EXISTS offer_template;
