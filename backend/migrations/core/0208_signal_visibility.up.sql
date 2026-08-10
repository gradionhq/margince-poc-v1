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
-- WHICH rows is the delicate part, and source_channel cannot decide it: the
-- contract DEFAULTS a human's own POST /signals to 'derived', so filtering on
-- it would sweep up filings a person wrote themselves and archive them. The
-- producer stamps source = 'signal-scan' and captured_by = 'agent:<kind>'; both
-- are required here, so nothing a human authored is touched. ghosted_thread is
-- excluded because it is computed from who spoke last and how long ago, which
-- its account's readers could count for themselves.
--
-- The cast is guarded. evidence is free-form jsonb, and a single row whose
-- source_id is not a canonical UUID would abort the cast — and with it the
-- migration, and with it the deploy. A value that cannot be read as an id
-- simply names no activity.
-- Every read and the write below are on tenant tables under FORCE row-level
-- security, so the sweep binds app.workspace_id per workspace: unbound, the
-- policy resolves to NULL and the UPDATE touches zero rows while reporting
-- success. The binding makes rows visible; the ws predicates scope the
-- statement, because a BYPASSRLS executor sees every workspace on every
-- iteration.
DO $$
DECLARE ws uuid;
BEGIN
FOR ws IN SELECT id FROM workspace LOOP
PERFORM set_config('app.workspace_id', ws::text, true);

WITH cited AS (
  SELECT s.id AS signal_id,
         CASE WHEN e->>'source_id' ~
                   '^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$'
              THEN (e->>'source_id')::uuid END AS activity_id
    FROM signal s
    -- The array test lives INSIDE the lateral, not in the WHERE below it. A
    -- WHERE clause does not order itself before the FROM it filters, so a
    -- single row whose evidence is an object rather than an array would reach
    -- jsonb_array_elements and abort the migration — and with it the deploy.
    -- Anything that is not an array simply cites nothing.
    CROSS JOIN LATERAL jsonb_array_elements(
      CASE WHEN jsonb_typeof(s.evidence) = 'array' THEN s.evidence ELSE '[]'::jsonb END) AS e
   WHERE s.source = 'signal-scan'
     AND s.captured_by LIKE 'agent:%'
     AND s.kind <> 'ghosted_thread'
     AND s.archived_at IS NULL
     AND s.workspace_id = ws
), readable AS (
  SELECT c.signal_id,
         -- The any-link rule, as auth.ActivityScopeClause spells it: one
         -- workspace-visible link makes the message readable to everyone who
         -- can see that record, and a summary of it discloses nothing further.
         bool_or(coalesce(vp.visibility, vo.visibility, 'workspace') <> 'owner') AS shared,
         -- The reader comes from a record that is actually capture-private.
         -- Taking any owner_id would narrow a shared finding to whoever happens
         -- to own a public contact on it, which withholds a card from everyone
         -- else for no reason at all.
         min(coalesce(vp.owner_id, vo.owner_id)::text)
           FILTER (WHERE coalesce(vp.visibility, vo.visibility) = 'owner')
           AS private_owner
    FROM cited c
    -- An inner join on purpose: a cited activity with NO links is readable by
    -- everyone under the link-less note rule, so its signal is left as it is.
    JOIN activity_link vl ON vl.activity_id = c.activity_id
    LEFT JOIN person vp ON vp.id = vl.person_id
    LEFT JOIN organization vo ON vo.id = vl.organization_id
   GROUP BY c.signal_id
)
UPDATE signal s
   SET visibility  = CASE WHEN r.private_owner IS NULL THEN s.visibility ELSE 'owner' END,
       owner_id    = CASE WHEN r.private_owner IS NULL THEN s.owner_id ELSE r.private_owner::uuid END,
       -- No nameable reader and nobody else may read the source: the row makes
       -- a claim nobody can be shown to be entitled to, so it stands down.
       archived_at = CASE WHEN r.private_owner IS NULL THEN now() ELSE s.archived_at END
  FROM readable r
 WHERE r.signal_id = s.id
   AND coalesce(r.shared, false) = false
   AND s.archived_at IS NULL
   AND s.workspace_id = ws;

END LOOP;
END $$;
