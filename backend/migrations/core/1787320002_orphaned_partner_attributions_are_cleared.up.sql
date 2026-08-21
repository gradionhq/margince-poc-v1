-- A deal may only name a company that is a partner. New writes are refused by
-- the store; this reaches the rows written before that check existed.
--
-- An attribution to a company with no live partner row could never earn
-- anything: commission prices from the margin tier on that row, so accrual
-- finds no tier and records a skip. The deal reads as credited on every screen
-- and pays nobody — and because the store now refuses that shape, the field
-- could not be corrected in place either.
--
-- Both halves are cleared together: deal_partner_attribution_pairing admits the
-- columns populated together or not at all, so clearing one alone would leave a
-- row the constraint rejects.
--
-- What is NOT done here: nothing is invented. A deal whose partner was never
-- set up keeps its history in the audit log, and the attribution can be made
-- again once that company has a partner programme.

SET LOCAL lock_timeout = '5s';

UPDATE deal
   SET partner_org_id = NULL,
       partner_attribution = NULL
 WHERE partner_org_id IS NOT NULL
   AND NOT EXISTS (
         SELECT 1 FROM partner
          WHERE partner.organization_id = deal.partner_org_id
            AND partner.archived_at IS NULL);
