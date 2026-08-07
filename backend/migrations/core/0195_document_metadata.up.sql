-- 0195: a file gains a small amount of meaning (DOC-DDL-1, documents-and-files).
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

-- The provider identity is a PAIR or it is absent. NULLs never collide in a
-- unique index, so with only the source id set every re-pull of the same
-- mailbox would insert another row and the idempotency this index exists to
-- give would hold for no file at all.
ALTER TABLE attachment
  ADD CONSTRAINT attachment_external_identity_complete
  CHECK ((external_source_id IS NULL) = (external_part_id IS NULL));

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

-- BACKFILL THE ROLL-UP FOR FILES THAT ALREADY EXIST.
--
-- Without this, every attachment on an installation that upgrades gets
-- organization_id = NULL while the account library filters strictly on it, so
-- contracts and offers already filed against a company or its deals vanish from
-- the account view. Nothing about that failure is visible: the library renders
-- an empty state, which is exactly what a company with no documents renders.
--
-- The workspace is bound per iteration because attachment carries FORCE row
-- level security with deny-on-unset semantics; unbound, the UPDATE's USING
-- clause resolves NULL and the statement reports success having changed nothing.
-- The predicate is separate and also required: a superuser or BYPASSRLS owner
-- (every dev machine and CI) is not filtered by the policy at all, so without it
-- the statement would run once per workspace over every workspace's rows.
--
-- A person's files roll up to nothing, matching the writer: person to
-- organization runs through relationship kind 'employment', which is
-- many-valued, so a contact at two companies has no single account.
DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);

    UPDATE attachment a
       SET organization_id = a.entity_id
     WHERE a.entity_type = 'organization'
       AND a.organization_id IS NULL
       AND a.workspace_id = ws;

    UPDATE attachment a
       SET organization_id = d.organization_id
      FROM deal d
     WHERE a.entity_type = 'deal'
       AND a.entity_id = d.id
       AND d.organization_id IS NOT NULL
       AND a.organization_id IS NULL
       AND a.workspace_id = ws;
  END LOOP;
END $$;
