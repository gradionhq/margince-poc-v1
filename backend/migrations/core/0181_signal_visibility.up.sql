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
-- Default 'workspace' is what a signal has always been, and it is right for
-- every row a HUMAN filed: they chose the account and wrote the words.
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

-- Narrow the rows a PRODUCER already wrote from what messages SAY.
--
-- The default above cannot serve these. A derived signal's words come from
-- correspondence rather than from a person who chose to publish them, and any
-- such row standing when this migration runs was written before the producer
-- knew to ask who may read it. Leaving it at the default keeps exactly the
-- disclosure this column was added to stop, on the one installation nobody
-- would think to check: the one that already had the feature.
--
-- Which rows: every derived kind EXCEPT ghosted_thread. That one is computed
-- from who spoke last and how long ago, which its account's readers could count
-- for themselves; the rest are written from message text.
--
-- This deliberately does NOT re-test whether the cited message happens to be
-- workspace-readable. The model is shown a whole conversation and writes from
-- all of it, while the row records only the message it cited — so a per-message
-- test can call a summary shareable when its siblings were not, which is the
-- one direction that must never be guessed. Over-narrowing costs a reader a
-- card they could have had; under-narrowing hands them correspondence that was
-- never theirs.
--
-- A row whose reader cannot be named is archived rather than left standing: it
-- makes a claim nobody can be shown to be entitled to.
WITH cited AS (
  SELECT s.id AS signal_id,
         (e->>'source_id')::uuid AS activity_id
    FROM signal s
    CROSS JOIN LATERAL jsonb_array_elements(s.evidence) AS e
   WHERE s.source_channel = 'derived'
     AND s.kind <> 'ghosted_thread'
     AND s.archived_at IS NULL
     AND jsonb_typeof(s.evidence) = 'array'
), reader AS (
  SELECT c.signal_id,
         min(coalesce(vp.owner_id, vo.owner_id)::text) AS private_owner
    FROM cited c
    -- An inner join on purpose: a cited activity with NO links is readable by
    -- everyone under the link-less note rule (auth.ActivityScopeClause reads an
    -- empty link set as visible), so its signal is left exactly as it is.
    JOIN activity_link vl ON vl.activity_id = c.activity_id
    LEFT JOIN person vp ON vp.id = vl.person_id
    LEFT JOIN organization vo ON vo.id = vl.organization_id
   GROUP BY c.signal_id
)
UPDATE signal s
   SET visibility  = CASE WHEN r.private_owner IS NULL THEN s.visibility ELSE 'owner' END,
       owner_id    = CASE WHEN r.private_owner IS NULL THEN s.owner_id ELSE r.private_owner::uuid END,
       archived_at = CASE WHEN r.private_owner IS NULL THEN now() ELSE s.archived_at END
  FROM reader r
 WHERE r.signal_id = s.id
   AND s.archived_at IS NULL;
