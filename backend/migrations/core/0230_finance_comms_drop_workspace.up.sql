-- 0230: the finance mirror and the outbound-comms ledger drop the tenant
-- column (ADR-0091 §8 phase D).
--
-- Six tables, six indexes, two redundant uniques. Neither module references
-- the other's tables, which is what makes them one reviewable unit.
--
-- The finance mirror's own uniques already key on `connection_id` — an
-- external customer, invoice or payment is unique within the connection that
-- mirrored it, and a connection belongs to the installation. Dropping the
-- tenant column changes nothing about what those answer; it removes a column
-- that was never part of the question.
--
-- `comms_outbound_workspace_activity_ix` narrows to `activity_id` alone, which
-- is the whole of what it looked up: the ledger row for an activity.

DROP INDEX finance_invoice_account_ix;
CREATE INDEX finance_invoice_account_ix ON finance_invoice (organization_id, issued_at DESC);

DROP INDEX finance_invoice_credits_ix;
CREATE INDEX finance_invoice_credits_ix ON finance_invoice (credits_invoice_id) WHERE credits_invoice_id IS NOT NULL;

DROP INDEX finance_invoice_open_ix;
CREATE INDEX finance_invoice_open_ix ON finance_invoice (organization_id) WHERE open_minor > 0 AND void_at IS NULL;

DROP INDEX finance_payment_account_ix;
CREATE INDEX finance_payment_account_ix ON finance_payment (organization_id, paid_at DESC);

DROP INDEX finance_payment_invoice_ix;
CREATE INDEX finance_payment_invoice_ix ON finance_payment (invoice_id) WHERE invoice_id IS NOT NULL;

DROP INDEX comms_outbound_workspace_activity_ix;
CREATE INDEX comms_outbound_workspace_activity_ix ON comms_outbound (activity_id);

ALTER TABLE finance_connection DROP CONSTRAINT uq_finance_connection_ws_id;
ALTER TABLE finance_invoice DROP CONSTRAINT uq_finance_invoice_ws_id;

ALTER TABLE finance_connection DROP COLUMN workspace_id;
ALTER TABLE finance_external_customer DROP COLUMN workspace_id;
ALTER TABLE finance_customer_link DROP COLUMN workspace_id;
ALTER TABLE finance_invoice DROP COLUMN workspace_id;
ALTER TABLE finance_payment DROP COLUMN workspace_id;
ALTER TABLE comms_outbound DROP COLUMN workspace_id;
