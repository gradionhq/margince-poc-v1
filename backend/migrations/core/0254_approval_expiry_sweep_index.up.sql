-- The expiry sweep runs every five minutes and asks one question:
-- which pending approvals are past their window. Without this index that is a
-- scan of every pending row plus a sort, forever, on a table whose pending set
-- grows with the installation.
--
-- Partial on status='pending' because the sweep never looks at anything else,
-- and the existing idx_approval_inbox is ordered by created_at, which answers
-- the inbox's question rather than this one.
CREATE INDEX IF NOT EXISTS idx_approval_expiry_due
    ON approval (expires_at)
    WHERE status = 'pending';
