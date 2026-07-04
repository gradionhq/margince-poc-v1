DO $$
BEGIN
  IF EXISTS (SELECT FROM pg_roles WHERE rolname = 'margince_app') THEN
    ALTER DEFAULT PRIVILEGES IN SCHEMA public
      REVOKE SELECT, INSERT, UPDATE, DELETE ON TABLES FROM margince_app;
    REVOKE ALL ON ALL TABLES IN SCHEMA public FROM margince_app;
    REVOKE USAGE ON SCHEMA public FROM margince_app;
  END IF;
END $$;
