-- Reverse of phase A: put tenant isolation back on every table that carries a
-- workspace_id and does not already have a policy.
--
-- This restores the PROTECTION, not the policy names. Every policy dropped by
-- the up migration held the identical tenant-isolation predicate, and the two
-- naming conventions in the tree (`<table>_tenant_isolation` from 0014,
-- `<table>_ws` from later enrolments) are not load-bearing: nothing references
-- a policy by name except a DROP. So the down writes one canonical name per
-- table rather than reconstructing which convention each table happened to
-- get, and an installation walked backwards is protected exactly as it was.
--
-- Each down enrols only tables that hold NO policy right now, so the two halves
-- cover a disjoint set whichever order they are reverted in — `down` reverts
-- per namespace, and core and custom do not interleave.
--
-- The two deliberate non-RLS tables stay out, for the reason they were always
-- out: `booking_page` and `preference_token` are slug/token→tenant RESOLVERS,
-- read to discover which workspace to bind BEFORE any GUC exists (data-model
-- §1.2). Enrolling them here would make the no-login preference centre and the
-- booking page unreadable rather than more secure. `rlsExemptTables` in
-- migrations/schema_fitness_integration_test.go is where that pair is ratified.

DO $$
DECLARE r record;
BEGIN
  FOR r IN
    SELECT c.relname AS table_name
      FROM pg_class c
      JOIN pg_namespace n ON n.oid = c.relnamespace
     WHERE n.nspname = current_schema() AND c.relkind = 'r'
       AND c.relname NOT IN ('booking_page', 'preference_token')
       AND NOT EXISTS (SELECT 1 FROM pg_policy p WHERE p.polrelid = c.oid)
       AND EXISTS (
         SELECT 1 FROM pg_attribute a
          WHERE a.attrelid = c.oid AND a.attname = 'workspace_id'
            AND a.attnum > 0 AND NOT a.attisdropped)
     ORDER BY c.relname
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', r.table_name);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', r.table_name);
    EXECUTE format(
      'CREATE POLICY %I ON %I '
      || 'USING (workspace_id = NULLIF(current_setting(''app.workspace_id'', true), '''')::uuid) '
      || 'WITH CHECK (workspace_id = NULLIF(current_setting(''app.workspace_id'', true), '''')::uuid)',
      r.table_name || '_tenant_isolation', r.table_name);
  END LOOP;
END $$;
