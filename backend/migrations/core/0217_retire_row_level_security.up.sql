-- ADR-0091 §8 phase A: retire row-level security across the whole schema.
--
-- Every one of the 139 policies is the same tenant-isolation predicate —
-- `workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid`
-- on USING and WITH CHECK alike (0014 and every enrolment since). There is no
-- row-scope policy among them, so nothing that scopes a read WITHIN an
-- organization is being dropped here: `platform/auth` was already the only
-- thing enforcing owner, team and grant scope, and it stays exactly as strong
-- (ADR-0091 §3).
--
-- What IS being dropped is deny-on-unset, and §4 names the cost rather than
-- mitigating it: after this, a store query that forgets its scope predicate
-- returns other users' rows instead of zero rows, and no test fails by
-- construction. The control that replaces it is `rbacgate_test.go`, whose
-- waiver list was re-read as security surface before this migration was
-- written — §3 makes that a precondition, not a follow-up.
--
-- Derived from the catalog, not from a list: 0014's hand-written enrolment
-- array is exactly the shape that rots, and this migration must reach every
-- table that has RLS today, whichever migration enrolled it.
--
-- It sweeps every non-system SCHEMA, not just this one, because the extension
-- tier keeps its tables in `ext` (extensions/notes) and they carry the same
-- tenant-isolation policy. Leaving those armed while the GUC that feeds them
-- goes away would not be conservative — deny-on-unset would make the table
-- unreadable rather than protected.
--
-- The custom namespace runs AFTER core, so the tables it creates do not exist
-- yet when this runs; `20260812120000_retire_row_level_security_custom` is its
-- other half. Both are catalog sweeps and idempotent, so running both in
-- either order is harmless.

-- Pre-flight (§8): refuse against a database holding more than one LIVE
-- workspace. ADR-0061 §3 already makes the API refuse to start in that state,
-- so this should be unreachable — but collapsing two tenants' rows into one
-- un-scoped table is the single failure in this change that no `down` can
-- undo, and "unreachable" is not a reason to leave it unchecked.
--
-- LIVE is the deliberate boundary, and what it leaves behind is worth saying
-- out loud rather than discovering later. Archiving a workspace does not
-- delete its rows: they keep their workspace_id in every tenant table, and
-- once the policies are gone they sit in the same un-scoped tables as the
-- remaining tenant's, readable by anything that reads the installation.
--
-- Counting them anyway would contradict the invariant this repo actually
-- implements. ADR-0061 §3 defines the single-organization rule on LIVE
-- workspaces, and archiving is the affordance the product gives an operator
-- for resolving to one — the upgrade-replay fixtures use exactly that. A
-- guard stricter than the rule it enforces would refuse the documented
-- upgrade path.
--
-- So the residue is accepted and named: an installation that archived a
-- previous tenant rather than deleting its rows keeps those rows visible
-- after this migration. An operator who does not want that deletes them
-- before running it; nothing here can tell the two intentions apart.
DO $$
DECLARE live int;
BEGIN
  SELECT count(*) INTO live FROM workspace WHERE archived_at IS NULL;
  IF live > 1 THEN
    RAISE EXCEPTION 'refusing to retire row-level security: % live workspaces. '
      'This installation holds more than one tenant, and dropping tenant '
      'isolation would make their rows mutually visible with no way back. '
      'Resolve to a single organization first (ADR-0061/A107).', live;
  END IF;
END $$;

DO $$
DECLARE r record;
BEGIN
  FOR r IN
    SELECT n.nspname AS schema, c.relname AS table_name, p.polname AS policy
      FROM pg_policy p
      JOIN pg_class c ON c.oid = p.polrelid
      JOIN pg_namespace n ON n.oid = c.relnamespace
     WHERE n.nspname NOT IN ('pg_catalog', 'information_schema')
       AND n.nspname NOT LIKE 'pg_%'
  LOOP
    EXECUTE format('DROP POLICY %I ON %I.%I', r.policy, r.schema, r.table_name);
  END LOOP;

  -- NO FORCE before DISABLE: the two flags are independent, and a table left
  -- FORCEd with row security disabled is a confusing state to land a failed
  -- run in — the policies are already gone by here, so neither flag protects
  -- anything.
  FOR r IN
    SELECT n.nspname AS schema, c.relname AS table_name
      FROM pg_class c
      JOIN pg_namespace n ON n.oid = c.relnamespace
     WHERE n.nspname NOT IN ('pg_catalog', 'information_schema')
       AND n.nspname NOT LIKE 'pg_%'
       AND c.relkind = 'r' AND c.relrowsecurity
  LOOP
    EXECUTE format('ALTER TABLE %I.%I NO FORCE ROW LEVEL SECURITY', r.schema, r.table_name);
    EXECUTE format('ALTER TABLE %I.%I DISABLE ROW LEVEL SECURITY', r.schema, r.table_name);
  END LOOP;
END $$;
