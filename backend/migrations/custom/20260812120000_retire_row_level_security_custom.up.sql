-- ADR-0091 §8 phase A, custom half. Core 0217 sweeps the policies that exist
-- when IT runs; the custom namespace runs afterwards, so the overlay cluster's
-- tables (20260716120000_overlay and its successors) and the flip/import-run
-- tables did not exist yet and kept theirs.
--
-- Same catalog-driven sweep, deliberately not a list of table names: this half
-- also has to reach whatever a fork added in its own custom migrations, and a
-- hand-written enumeration would silently miss it.
--
-- No pre-flight here: core 0217 already refused the >1-workspace database
-- before any policy was dropped, and this migration cannot run without it.
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
