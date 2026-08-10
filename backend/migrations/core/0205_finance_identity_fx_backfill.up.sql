-- Backfill the identity FX rate onto invoices the mirror already wrote.
--
-- The mirror never recorded fx_rate_to_base, so every mirrored invoice reads as
-- unconvertible and the summary formulas refuse the whole total (FIN-AC-6). The
-- writer is fixed, but the fix alone does not reach these rows: the sync skips
-- an invoice whose source values are unchanged, and the source of an already
-- mirrored invoice does not change just because we started recording a rate. A
-- deployed installation would keep an empty finance card forever.
--
-- ONLY the identity, matching the writer exactly: an invoice issued in the
-- workspace's own reporting currency is already in base, and its rate froze on
-- the day it was issued. A foreign invoice still gets no rate, because this
-- build has no rate sheet and inventing one would sum dollars into euros
-- silently. The rate and its date are written together — finance_invoice_fx_pair
-- CHECKs that they are one fact (FIN-PARAM-7).
DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);

    UPDATE finance_invoice
       SET fx_rate_to_base = 1,
           fx_rate_date    = finance_invoice.issued_at
      FROM workspace w
     WHERE w.id = finance_invoice.workspace_id
       AND finance_invoice.fx_rate_to_base IS NULL
       AND upper(trim(finance_invoice.currency)) = upper(trim(w.base_currency))
       -- The statement's own scope. Binding the GUC makes the rows VISIBLE; it
       -- does not filter them, and every dev machine and CI run migrates as a
       -- superuser or BYPASSRLS role that RLS does not apply to at all. Without
       -- this the update would run once per workspace over every workspace.
       AND finance_invoice.workspace_id = ws;
  END LOOP;
END $$;
