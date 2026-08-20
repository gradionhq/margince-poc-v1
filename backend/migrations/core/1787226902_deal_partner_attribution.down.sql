-- Reverses 1787226902.
--
-- partner_org_id survives; only the attribution it carried is dropped, which
-- returns the deal to "a partner is named, what they did is unrecorded".
--
-- Dropping the trigger and the column both take ACCESS EXCLUSIVE on tables this
-- migration did not create, so the wait is bounded: an unbounded one lets a
-- single idle-in-transaction session stall every write to deal and organization
-- for as long as the migration is willing to queue.
SET LOCAL lock_timeout = '3s';

DROP TRIGGER IF EXISTS organization_delete_clears_deal_partner ON organization;
DROP FUNCTION IF EXISTS deal_clear_partner_attribution_on_org_delete();

DROP INDEX IF EXISTS idx_deal_partner_attribution;

ALTER TABLE deal DROP CONSTRAINT IF EXISTS deal_partner_attribution_pairing;
ALTER TABLE deal DROP CONSTRAINT IF EXISTS deal_partner_attribution_check;
ALTER TABLE deal DROP COLUMN IF EXISTS partner_attribution;
