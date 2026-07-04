-- One-time cluster setup (run by `make db-init` as the database owner):
-- the runtime role the server connects as. Non-superuser, no BYPASSRLS,
-- not the table owner — RLS binds it with no exception path.
DO $$
BEGIN
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'margince_app') THEN
    CREATE ROLE margince_app LOGIN PASSWORD 'margince_app_dev';
  END IF;
END $$;
