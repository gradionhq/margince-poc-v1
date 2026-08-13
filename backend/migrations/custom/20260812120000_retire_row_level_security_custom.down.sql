-- Reverse of the custom half: tenant isolation back on the custom namespace's
-- own workspace_id tables, with the same canonical policy name core 0217's
-- down uses and for the same reason — every policy dropped held the identical
-- predicate, and the name was never referenced by anything but a DROP.
--
-- Enrols only tables that hold NO policy right now, which is what keeps the two
-- halves disjoint however they are ordered: whichever runs first takes the
-- tables that are still bare, and the second finds them already enrolled and
-- skips them rather than colliding on the policy name.
DO $$
DECLARE r record;
BEGIN
  FOR r IN
    SELECT n.nspname AS schema, c.relname AS table_name
      FROM pg_class c
      JOIN pg_namespace n ON n.oid = c.relnamespace
     WHERE n.nspname NOT IN ('pg_catalog', 'information_schema')
       AND n.nspname NOT LIKE 'pg_%'
       AND c.relkind = 'r'
       AND c.relname NOT IN ('booking_page', 'preference_token')
       AND NOT EXISTS (SELECT 1 FROM pg_policy p WHERE p.polrelid = c.oid)
       AND EXISTS (
         SELECT 1 FROM pg_attribute a
          WHERE a.attrelid = c.oid AND a.attname = 'workspace_id'
            AND a.attnum > 0 AND NOT a.attisdropped)
     ORDER BY n.nspname, c.relname
  LOOP
    EXECUTE format('ALTER TABLE %I.%I ENABLE ROW LEVEL SECURITY', r.schema, r.table_name);
    EXECUTE format('ALTER TABLE %I.%I FORCE ROW LEVEL SECURITY', r.schema, r.table_name);
    EXECUTE format(
      'CREATE POLICY %I ON %I.%I '
      || 'USING (workspace_id = NULLIF(current_setting(''app.workspace_id'', true), '''')::uuid) '
      || 'WITH CHECK (workspace_id = NULLIF(current_setting(''app.workspace_id'', true), '''')::uuid)',
      r.table_name || '_tenant_isolation', r.schema, r.table_name);
  END LOOP;
END $$;
