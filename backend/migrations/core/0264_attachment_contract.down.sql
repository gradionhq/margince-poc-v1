DROP INDEX IF EXISTS attachment_contract_ix;
ALTER TABLE attachment DROP COLUMN IF EXISTS contract_id;
