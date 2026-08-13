-- 0222: what KIND of sender a first-time address turned out to be, recorded
-- beside — never instead of — the lifecycle status.
--
-- The verdict engine could answer only `real` or `noise`, and its prompt put "a
-- person or company" on the same side of that line. So every above-floor `real`
-- ran the person-creation path, and a company-branded envelope, an
-- organization's shared mailbox and a service address all became PEOPLE. A real
-- import produced contacts named "Docsign" (on a vendor's support address),
-- "VINASA" (a trade association's contact@), "Expensify" and "Support".
--
-- status stays exactly what it was: the row's lifecycle, driving the partial
-- unique indexes, the due-claim, prior-disposition logic, the noise sweeps and
-- the review reconciliation. kind is the orthogonal question — WHO wrote —
-- which the engine could not express before and which decides what gets
-- created. Folding the two into one column would have made every one of those
-- lifecycle queries wrong.
--
-- Nullable because it is only ever known for a row a model or a human actually
-- judged: a row still pending has no kind, and inventing one would claim an
-- answer nobody gave.

ALTER TABLE capture_pending_counterparty
  ADD COLUMN kind text NULL
    CHECK (kind IS NULL OR kind IN (
      -- A human with an interest in this business: the only kind that becomes
      -- a person record.
      'person',
      -- A mailbox an organization answers rather than a person — support@,
      -- info@, a shared team address. Real correspondence, no human to name.
      'role_mailbox',
      -- The organization itself writing under its own name.
      'organization_sender',
      -- Bulk editorial mail, however welcome. A newsletter subscription is not
      -- a business relationship.
      'newsletter',
      -- Automated mail from a service: receipts, notifications, delivery
      -- reports.
      'transactional',
      -- Unsolicited commercial mail, including the kind a human replied to in
      -- order to decline it.
      'spam'));

COMMENT ON COLUMN capture_pending_counterparty.kind IS
  'What kind of sender this address turned out to be. Orthogonal to status, which is the row lifecycle.';
