-- One-time database bootstrap. Run ONCE against your Postgres as a superuser
-- BEFORE the first api deploy, then never again. For example:
--
--   psql "postgres://postgres:…@<host>:5432/postgres" \
--     -v owner_pw="$OWNER_PW" -v app_pw="$APP_PW" -f scripts/deploy/db-bootstrap.sql
--
-- Pass the two role passwords RAW (not pre-quoted) as psql variables so they
-- never land in this committed file — `:'…'` + `%L` quote and escape them. They
-- MUST match the passwords embedded in the app's MARGINCE_OWNER_DSN / MARGINCE_DSN.
--
-- Why two non-superuser roles: margince enforces tenant isolation with
-- FORCE ROW LEVEL SECURITY, which a superuser (and only a superuser) bypasses.
--   * margince_owner — owns the database + tables, runs migrations (DDL). Not a
--     superuser, no BYPASSRLS: RLS binds it too.
--   * margince_app   — the runtime role the api/worker connect as. Its table
--     grants are applied by migration 0015_app_role_grants, which is a no-op
--     unless the role already exists — hence it is created here, first.
--
-- Idempotent: safe to re-run (each step guards on existence).

\set ON_ERROR_STOP on

-- The two roles. psql does NOT interpolate `:'var'` inside a dollar-quoted
-- DO $$…$$ body, so the guarded CREATE ROLE is built in a plain SELECT (where
-- interpolation DOES happen) and run with \gexec. `format(… %L …)` safely quotes
-- the password; the WHERE NOT EXISTS makes each idempotent (an existing role
-- yields no row, so \gexec runs nothing). Neither role is a superuser or granted
-- BYPASSRLS — FORCE RLS must bind them.

-- The runtime app role (mirrors scripts/db-init.sql for local dev).
SELECT format('CREATE ROLE margince_app LOGIN PASSWORD %L', :'app_pw')
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'margince_app')
\gexec

-- The owner role that runs migrations and owns every object.
SELECT format('CREATE ROLE margince_owner LOGIN PASSWORD %L', :'owner_pw')
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'margince_owner')
\gexec

-- The application database, owned by margince_owner. CREATE DATABASE cannot run
-- inside a DO block or a transaction, so it is guarded with \gexec instead.
SELECT 'CREATE DATABASE margince OWNER margince_owner'
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'margince')
\gexec

-- The app role must be able to reach the database; object-level grants come from
-- migration 0015 (run by the api entrypoint as margince_owner).
GRANT CONNECT ON DATABASE margince TO margince_app;

-- Extensions the migrations expect. `vector` (pgvector) is NOT a trusted
-- extension, so it cannot be installed by the non-superuser owner from a
-- migration — pre-install it (and the trusted ones too, so every migration's
-- `CREATE EXTENSION IF NOT EXISTS` is a guaranteed no-op) here as superuser.
\connect margince
CREATE EXTENSION IF NOT EXISTS vector;      -- 0022_embeddings (pgvector; untrusted)
CREATE EXTENSION IF NOT EXISTS unaccent;    -- 0052_fts_linguistics
CREATE EXTENSION IF NOT EXISTS pg_trgm;     -- 0052_fts_linguistics
CREATE EXTENSION IF NOT EXISTS btree_gist;  -- 0032_meeting_exclusion
