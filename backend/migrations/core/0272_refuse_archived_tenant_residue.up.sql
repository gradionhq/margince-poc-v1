-- 0272: refuse to go further while an ARCHIVED tenant still holds records.
--
-- This is a gate, not a change. It exists because ADR-0091 §8 phase D does
-- something 0217 did not, and nothing so far has said so out loud.
--
-- 0217 refused more than one LIVE workspace and then named the residue it
-- accepted: an installation that ARCHIVED a previous tenant rather than
-- deleting its rows keeps those rows, visible once the policies drop. That was
-- a statement about visibility, and it was true.
--
-- Dropping the column is a different act. After it, an archived tenant's person
-- is not a visible foreign row — it is indistinguishable from one of ours. It
-- lists, searches, exports, and ages out under our retention policy as if it
-- had always been ours. And it is one-way: the reverse migrations restore the
-- COLUMN, but they backfill every row to the live workspace, because by then
-- nothing remains to reconstruct the original from.
--
-- So the operator decides, before the record tables lose theirs. Delete that
-- data, or export it, or accept it by deleting the archived workspace rows —
-- each is a deliberate act, and none of them is one a migration should perform
-- on somebody's behalf.
--
-- WHAT THIS DOES NOT COVER, stated because the gap is real: thirteen modules
-- already dropped their column before this gate existed (signals, collections,
-- quotas, customfields, finance, comms, consent, approvals, automation, voice,
-- webhooks, agents, privacy). Their rows merged silently. They are
-- configuration and machinery rather than records, which is why the stakes were
-- low, but "low" is not "none" and a reader should not have to infer it from
-- migration numbers.
--
-- The append-only ledgers are exempt BY NAME rather than by accident. audit_log
-- and system_log carry an immutability trigger that forbids DELETE, so an
-- installation could not clear residue there even if it wanted to; demanding it
-- would be demanding the impossible. Their attribution is history, not records,
-- and it goes with the ledger's own column at the end of phase D.
DO $$
DECLARE
  offending text;
  found     text := '';
  n         bigint;
BEGIN
  -- Derived from the catalog rather than listed: a table that still carries the
  -- column is exactly a table this gate is about, and a hand-kept list would go
  -- stale in the direction that matters — silently missing one.
  FOR offending IN
    SELECT c.relname
    FROM pg_class c
    JOIN pg_namespace n ON n.oid = c.relnamespace
    JOIN pg_attribute a ON a.attrelid = c.oid
    WHERE n.nspname = 'public'
      AND c.relkind = 'r'
      AND a.attname = 'workspace_id'
      AND a.attnum > 0 AND NOT a.attisdropped
      AND c.relname NOT IN ('audit_log', 'system_log')
    ORDER BY c.relname
  LOOP
    EXECUTE format(
      'SELECT count(*) FROM %I t JOIN workspace w ON w.id = t.workspace_id WHERE w.archived_at IS NOT NULL',
      offending) INTO n;
    IF n > 0 THEN
      found := found || format('%s (%s rows), ', offending, n);
    END IF;
  END LOOP;

  IF found <> '' THEN
    RAISE EXCEPTION 'refusing to continue: an archived workspace still holds rows in %',
      rtrim(found, ', ')
      USING HINT =
        'ADR-0091 phase D drops workspace_id from these tables, after which those rows are '
        'indistinguishable from this installation''s own — they will list, search, export and '
        'age out as if they had always been yours, and no rollback can separate them again. '
        'Decide first: delete the archived tenants'' rows, export them, or delete the archived '
        'workspace rows to accept the merge. Then run this migration again.';
  END IF;
END $$;
