-- 0194: a file gains a small amount of meaning (DOC-DDL-1, documents-and-files).
--
-- Until now an attachment carried a filename, bytes and the record it hung off,
-- and nothing else. No category, no notion of which version is current, no way
-- to pin the one that matters — so a rep looking for "the signed contract" on an
-- account with forty files had the filename and their memory.
--
-- CURRENT IS ASSERTED, NEVER INFERRED. doc_state is set by a human or by the
-- source that produced the file. Nothing here derives currency from the newest
-- upload date or from a filename containing the word "final": the most recent
-- upload is very often a draft, "final-v3" is a joke everyone has made, and an
-- inference would be a confident wrong answer to the exact question this exists
-- to answer. Hence the default is `current` for a single file and a human moves
-- it, rather than a rule that guesses.
ALTER TABLE attachment
  ADD COLUMN category text NOT NULL DEFAULT 'other'
    CHECK (category IN ('contract','offer','legal','email_attachment','other')),
  -- A display name distinct from the original filename. `Q3 renewal — signed`
  -- is what a reader looks for; `scan_20260612_0001.pdf` is what arrived.
  ADD COLUMN title text NULL,
  ADD COLUMN doc_state text NOT NULL DEFAULT 'current'
    CHECK (doc_state IN ('draft','current','final','superseded')),
  ADD COLUMN pinned boolean NOT NULL DEFAULT false,
  ADD COLUMN supersedes_id uuid NULL,
  -- A ROLL-UP READ PATH, not a second parent. The primary parent stays the
  -- record the file was attached to and keeps owning its visibility; this makes
  -- the account aggregation affordable at a hundred documents. It is maintained
  -- on relink and merge — when an activity moves to another company its files
  -- move with it, or the document stays filed under a company it left.
  ADD COLUMN organization_id uuid NULL,
  ADD COLUMN deal_id uuid NULL,
  ADD COLUMN project_id uuid NULL,
  ADD COLUMN activity_id uuid NULL,
  -- The provider's message identity and the part's identity within it. Capture
  -- is idempotent on the pair, so re-pulling a mailbox does not duplicate files.
  ADD COLUMN external_source_id text NULL,
  ADD COLUMN external_part_id text NULL,
  -- Kept only when the declared content type disagreed with the sniffed one, so
  -- the disagreement stays inspectable rather than being silently resolved.
  ADD COLUMN declared_type text NULL;

-- A file cannot supersede itself. The deeper cycle is not expressible as a row
-- CHECK and is guarded in the write path; this closes the one-step case, which
-- is the one a careless update actually produces.
ALTER TABLE attachment
  ADD CONSTRAINT attachment_supersedes_not_self
  CHECK (supersedes_id IS NULL OR supersedes_id <> id);

-- The composite target key a tenant-local self-reference needs: a cross-workspace
-- supersedes pointer is refused by the database rather than by a store check.
ALTER TABLE attachment ADD CONSTRAINT uq_attachment_ws_id UNIQUE (workspace_id, id);
ALTER TABLE attachment
  ADD CONSTRAINT attachment_supersedes_fkey
  FOREIGN KEY (workspace_id, supersedes_id) REFERENCES attachment (workspace_id, id)
  ON DELETE SET NULL (supersedes_id);

CREATE UNIQUE INDEX attachment_external_part_key
  ON attachment (workspace_id, external_source_id, external_part_id)
  WHERE external_source_id IS NOT NULL;

-- Pinned first, then newest: the order the account library renders in, so the
-- read is an index scan rather than a sort over every file on the account.
CREATE INDEX attachment_account_ix
  ON attachment (workspace_id, organization_id, pinned DESC, created_at DESC)
  WHERE archived_at IS NULL;

COMMENT ON COLUMN attachment.doc_state IS
  'Asserted, never inferred (DOC-DDL-1): a human or the producing source sets it.';
COMMENT ON COLUMN attachment.organization_id IS
  'Account roll-up read path, NOT a second parent — visibility stays the primary parent''s.';
