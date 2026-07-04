-- Grants for the runtime application role (data-model §1.3: the app
-- connects as a NON-owner, non-superuser role so RLS actually binds it;
-- FORCE additionally binds the owner, belt and braces). The role itself
-- is cluster-level and created by scripts/db-init.sql, not a migration;
-- this grant block is conditional so the schema also applies on databases
-- that run everything as the owner (throwaway test databases).

DO $$
BEGIN
  IF EXISTS (SELECT FROM pg_roles WHERE rolname = 'margince_app') THEN
    GRANT USAGE ON SCHEMA public TO margince_app;
    GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO margince_app;
    -- audit_log is append-only: the immutability trigger enforces it for
    -- every role; revoking UPDATE/DELETE keeps the failure earlier and cheaper.
    REVOKE UPDATE, DELETE ON audit_log FROM margince_app;
    ALTER DEFAULT PRIVILEGES IN SCHEMA public
      GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO margince_app;
  END IF;
END $$;
