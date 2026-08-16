-- 0265: `contract.captured_by` holds the PRINCIPAL, not a user row.
--
-- 0262 typed it `uuid REFERENCES app_user(id)`, which every contract write then
-- failed against: the principal a store stamps is prefixed — "human:<id>" for a
-- session, "agent:<id>" for a passport — because an agent acting on somebody's
-- behalf is not a row in app_user. A foreign key there refuses the write rather
-- than recording who did it, and the refusal reached the caller as a 500.
--
-- Additive rather than an edit to 0262, which has shipped: editing an applied
-- migration changes what a FRESH installation gets while every deployed
-- database keeps the old column, and the two diverge in silence.
--
-- No data survives the change because nothing could have been written: the
-- column made every insert fail, so the table is empty wherever 0262 landed.
ALTER TABLE contract DROP CONSTRAINT IF EXISTS contract_captured_by_fkey;

ALTER TABLE contract
  ALTER COLUMN captured_by TYPE text USING captured_by::text;

ALTER TABLE contract
  ALTER COLUMN captured_by SET NOT NULL;

COMMENT ON COLUMN contract.captured_by IS
  'The principal that recorded this agreement, prefixed by kind ("human:<id>" / "agent:<id>") exactly as every other table spells it. Never a bare user id: an agent under a passport is not a row in app_user.';
