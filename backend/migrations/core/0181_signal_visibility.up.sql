-- A signal can now be narrower than the account it is about.
--
-- Until now a signal's visibility was entirely the subject record's, and that
-- was sound because a signal's evidence could only come from records at least
-- as visible as its subject: the producers reached an account through a direct
-- activity_link row, which is the same link that makes an activity readable to
-- that account's readers (auth.ActivityScopeClause is the any-link rule).
--
-- The producers now reach an account through the employer of the contact a
-- message is filed against, and through its deal — neither of which is a link
-- on the activity. Capture files mail against contacts it auto-creates as
-- owner-private (ADR-0063 §7), so a model summary of that correspondence would
-- otherwise be readable by everyone who can see the account while the
-- correspondence itself is readable by one person. Capture privacy does not
-- yield to row_scope=all (founder decision 2026-07-31), and a summary of a
-- private message discloses the message.
--
-- So a signal drawn from correspondence nobody else may read is owner-private
-- too, and says whose. Deterministic signals — the ones computed from who spoke
-- last and how long ago — carry no content and stay workspace-shared.
--
-- Default 'workspace': every existing row is a human's own filing or a
-- comparison over metadata, and neither is narrowed by this.
ALTER TABLE signal ADD COLUMN visibility text NOT NULL DEFAULT 'workspace'
  CHECK (visibility IN ('workspace','owner'));

-- The reader an owner-private signal answers to. Composite, so the database
-- refuses an owner from another workspace; SET NULL on the account column alone
-- because workspace_id is NOT NULL.
ALTER TABLE signal ADD COLUMN owner_id uuid;
ALTER TABLE signal
  ADD CONSTRAINT signal_owner_fkey
  FOREIGN KEY (workspace_id, owner_id) REFERENCES app_user (workspace_id, id)
  ON DELETE SET NULL (owner_id);

-- Owner-private with no owner would answer to nobody, which is not a stricter
-- signal but a lost one. The pairing is the invariant, so the database holds it
-- rather than every writer remembering to.
ALTER TABLE signal
  ADD CONSTRAINT signal_owner_private_names_its_owner
  CHECK (visibility <> 'owner' OR owner_id IS NOT NULL);

-- Owner-private rows are read by exactly one person, so the index that serves
-- the page's list carries the owner alongside the account it filters on.
CREATE INDEX idx_signal_owner_private ON signal (workspace_id, owner_id)
  WHERE visibility = 'owner' AND archived_at IS NULL;
