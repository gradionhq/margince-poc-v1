-- 0267: every deal already won gets the reason it was won without paper.
--
-- The point of 0266 is that "how many won deals have no agreement, and why"
-- becomes answerable. Without this backfill the answer starts as a column full
-- of NULLs — indistinguishable from "not yet asked" — and the report would be a
-- lie by omission on the day it shipped.
--
-- Every historical win is `imported`: these deals closed before the product
-- asked the question, so that is precisely what happened to them. Guessing a
-- more specific reason would invent facts about closes nobody recorded.
--
-- Deals that DO have a signed agreement with its paper attached are left NULL,
-- because NULL is the honest answer there: they have a contract, so there is no
-- reason to give. On a fresh installation that set is empty and the whole
-- statement is a no-op.
--
-- The tenant loop and the per-statement predicate are the migration-write rule:
-- `deal` still carries workspace_id, and an UPDATE that binds the workspace
-- without also scoping to it runs once per workspace against every row.
DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);

    UPDATE deal SET won_without_contract_reason = 'imported'
    WHERE status = 'won'
      AND won_without_contract_reason IS NULL
      AND deal.workspace_id = ws
      AND NOT EXISTS (
        SELECT 1 FROM contract c
        JOIN attachment a ON a.contract_id = c.id
        WHERE c.deal_id = deal.id
          AND c.archived_at IS NULL
          AND c.signed_on IS NOT NULL
          AND a.archived_at IS NULL
          AND a.category IN ('contract', 'legal')
          AND a.doc_state IN ('current', 'final'));
  END LOOP;
END $$;
