-- seed-reset.sql — wipe the demo workspace (slug 'demo-workspace') so
-- `make seed-dev` can rebuild it from scratch. Run by `make seed-reset`
-- against the compose stack's Postgres.
--
-- Deletes every row scoped to the demo workspace across all tenant tables
-- (those with a workspace_id column), discovered dynamically so a new
-- table is covered without touching this file. Workspaces other than the
-- demo one are untouched.
--
-- session_replication_role = replica disables FK enforcement and triggers
-- for the duration, so the deletes are order-independent. That includes
-- audit_log's append-only guard — correct here, because the reset erases
-- the whole tenant, history included. Requires superuser (the compose
-- stack's margince_owner is one).

BEGIN;

SET LOCAL session_replication_role = replica;

DO $$
DECLARE
  ws uuid;
  t  text;
BEGIN
  SELECT id INTO ws FROM workspace WHERE slug = 'demo-workspace';
  IF ws IS NULL THEN
    RAISE NOTICE 'seed-reset: no demo-workspace row — nothing to do';
    RETURN;
  END IF;

  -- Tenant tables carry FORCE RLS, which binds even a non-superuser table
  -- owner; bind the GUC so the deletes see the rows on such a connection.
  PERFORM set_config('app.workspace_id', ws::text, true);

  FOR t IN
    SELECT c.table_name
    FROM information_schema.columns c
    JOIN information_schema.tables tb
      ON tb.table_schema = c.table_schema AND tb.table_name = c.table_name
    WHERE c.table_schema = 'public'
      AND c.column_name = 'workspace_id'
      AND tb.table_type = 'BASE TABLE'
  LOOP
    EXECUTE format('DELETE FROM %I WHERE workspace_id = %L', t, ws);
  END LOOP;

  DELETE FROM workspace WHERE id = ws;
END $$;

COMMIT;
