-- The backfilled reason is indistinguishable from one a human chose, so the
-- down migration clears only the value this migration writes.
DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);
    UPDATE deal SET won_without_contract_reason = NULL
    WHERE won_without_contract_reason = 'imported' AND deal.workspace_id = ws;
  END LOOP;
END $$;
