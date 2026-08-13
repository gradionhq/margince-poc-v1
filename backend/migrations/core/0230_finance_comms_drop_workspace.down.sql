-- Reverse of 0230: the six tables carry the tenant column again.
--
-- The backfill reads the LIVE workspace, and the predicate is the point: 0217's
-- pre-flight refuses to run against a database holding more than one workspace
-- with archived_at IS NULL, so there is exactly one live row — but an
-- installation that resolved to one organization by ARCHIVING the others still
-- has those rows, and 0217 names that residue explicitly. Ordering by
-- created_at alone would hand every restored row to whichever workspace
-- happened to be created first, archived or not. If
-- `workspace` is empty and a table is not, SET NOT NULL fails and the rollback
-- stops — the honest outcome, since no value this migration could write would
-- be true.

ALTER TABLE finance_connection ADD COLUMN workspace_id uuid;
ALTER TABLE finance_external_customer ADD COLUMN workspace_id uuid;
ALTER TABLE finance_customer_link ADD COLUMN workspace_id uuid;
ALTER TABLE finance_invoice ADD COLUMN workspace_id uuid;
ALTER TABLE finance_payment ADD COLUMN workspace_id uuid;
ALTER TABLE comms_outbound ADD COLUMN workspace_id uuid;

DO $$
DECLARE ws uuid := (SELECT id FROM workspace WHERE archived_at IS NULL ORDER BY created_at LIMIT 1);
BEGIN
  UPDATE finance_connection SET workspace_id = ws;
  UPDATE finance_external_customer SET workspace_id = ws;
  UPDATE finance_customer_link SET workspace_id = ws;
  UPDATE finance_invoice SET workspace_id = ws;
  UPDATE finance_payment SET workspace_id = ws;
  UPDATE comms_outbound SET workspace_id = ws;
END $$;

ALTER TABLE finance_connection ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE finance_external_customer ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE finance_customer_link ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE finance_invoice ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE finance_payment ALTER COLUMN workspace_id SET NOT NULL;
ALTER TABLE comms_outbound ALTER COLUMN workspace_id SET NOT NULL;

ALTER TABLE finance_connection ADD CONSTRAINT finance_connection_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE finance_external_customer ADD CONSTRAINT finance_external_customer_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE finance_customer_link ADD CONSTRAINT finance_customer_link_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE finance_invoice ADD CONSTRAINT finance_invoice_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE finance_payment ADD CONSTRAINT finance_payment_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;
ALTER TABLE comms_outbound ADD CONSTRAINT comms_outbound_workspace_id_fkey
  FOREIGN KEY (workspace_id) REFERENCES workspace(id) ON DELETE RESTRICT;

ALTER TABLE finance_connection ADD CONSTRAINT uq_finance_connection_ws_id UNIQUE (id);
ALTER TABLE finance_invoice ADD CONSTRAINT uq_finance_invoice_ws_id UNIQUE (id);

DROP INDEX finance_invoice_account_ix;
CREATE INDEX finance_invoice_account_ix ON finance_invoice (workspace_id, organization_id, issued_at DESC);

DROP INDEX finance_invoice_credits_ix;
CREATE INDEX finance_invoice_credits_ix ON finance_invoice (workspace_id, credits_invoice_id) WHERE credits_invoice_id IS NOT NULL;

DROP INDEX finance_invoice_open_ix;
CREATE INDEX finance_invoice_open_ix ON finance_invoice (workspace_id, organization_id) WHERE open_minor > 0 AND void_at IS NULL;

DROP INDEX finance_payment_account_ix;
CREATE INDEX finance_payment_account_ix ON finance_payment (workspace_id, organization_id, paid_at DESC);

DROP INDEX finance_payment_invoice_ix;
CREATE INDEX finance_payment_invoice_ix ON finance_payment (workspace_id, invoice_id) WHERE invoice_id IS NOT NULL;

DROP INDEX comms_outbound_workspace_activity_ix;
CREATE INDEX comms_outbound_workspace_activity_ix ON comms_outbound (workspace_id, activity_id);
