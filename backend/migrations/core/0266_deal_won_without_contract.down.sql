DROP INDEX IF EXISTS deal_won_without_contract_ix;
ALTER TABLE deal
  DROP CONSTRAINT IF EXISTS deal_won_without_contract_only_when_won,
  DROP CONSTRAINT IF EXISTS deal_won_without_contract_detail,
  DROP CONSTRAINT IF EXISTS deal_won_without_contract_reason;
ALTER TABLE deal
  DROP COLUMN IF EXISTS won_without_contract_detail,
  DROP COLUMN IF EXISTS won_without_contract_reason;
