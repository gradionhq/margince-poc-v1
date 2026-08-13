-- Dropping the column drops every kind ever recorded. The lifecycle status is
-- untouched, so the ledger still says what happened to each address — only the
-- finer answer about who wrote is lost.

ALTER TABLE capture_pending_counterparty DROP COLUMN kind;
