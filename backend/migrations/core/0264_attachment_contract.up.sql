-- 0264: `attachment.contract_id` — which agreement a document is about
-- (ADR-0109/A160, CONTRACT-DDL-5).
--
-- A ROLL-UP READ PATH, exactly like the organization/deal/project columns
-- beside it (0195). The primary parent stays the record the file was attached
-- to and keeps owning its visibility; this column answers "show me the paper
-- for THIS agreement" without a second parent and without a second authority.
--
-- It is never the column authorization is decided from. A contract's own
-- visibility is inherited from its deal or organization, and a document's is
-- its primary parent's; pointing one at the other does not merge them.
--
-- Nullable and unbackfilled on purpose. Every document that predates contracts
-- is about no particular agreement, and guessing from a filename or a date is
-- exactly the inference the documents chapter refuses.
ALTER TABLE attachment
  ADD COLUMN contract_id uuid NULL REFERENCES contract(id) ON DELETE SET NULL;

COMMENT ON COLUMN attachment.contract_id IS
  'The agreement this document is about (ADR-0109). A roll-up read path, not a second parent: visibility stays the primary parent''s.';

-- The contract's own paper, newest first. Partial on both columns because a
-- document that names no contract is never in this read, and an archived one
-- never is either.
CREATE INDEX attachment_contract_ix
  ON attachment (contract_id, created_at DESC)
  WHERE contract_id IS NOT NULL AND archived_at IS NULL;
