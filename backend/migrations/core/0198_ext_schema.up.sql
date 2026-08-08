-- 0198: the ext schema — where every extension's own tables live (ADR-0069).
--
-- A separate schema, not more tables in public, is what lets the catalog gate
-- enumerate ext's contents and see exactly what a unit created. Inside ext,
-- tables stay prefixed ext_<name>_<table> so two units sharing the schema
-- cannot collide or address each other's data.
--
-- WHERE THE ext_<name> ROLE EXISTS TODAY, precisely. The gate
-- (backend/tools/extmigrategate) mints it against a throwaway database with
-- CREATE, USAGE on ext ONLY and no grants on public, applies the unit's
-- migrations as it, and so has PostgreSQL refuse a migration that touches a
-- core relation. At RUNTIME there is no such role yet: cmd/migrate opens one
-- margince_owner connection with no SET ROLE, so the tables below are created
-- and owned by margince_owner. Tenant isolation at runtime rests on FORCE row
-- level security and the workspace-bound policy each unit's migration
-- declares — which the gate also verifies — not on ownership. Issue #628
-- tracks minting the per-unit runtime role, which is what would turn
-- ownership into the DDL boundary and make DROP OWNED BY a purge primitive.
--
-- The core keeps owning public: this migration adds a namespace, it does not
-- move anything. The x_ COLUMN namespace on core tables is unrelated and
-- untouched.
CREATE SCHEMA IF NOT EXISTS ext;

-- USAGE only. The application role reads and writes extension tables through
-- grants each unit's own migration issues; it never creates objects here and
-- is not the owner, so the app's compromise does not become schema authority.
GRANT USAGE ON SCHEMA ext TO margince_app;

-- The comment ships INSIDE the database, where an operator reads it with \dn+
-- and has no way to check it against the repository, so it claims only what
-- actually holds: tables owned by whoever ran the migration (margince_owner
-- today, see above), isolated by RLS rather than by ownership.
COMMENT ON SCHEMA ext IS
  'Extension tables (ADR-0069): ext_<name>_<table>, applied by the migrate role from each enabled unit''s own migrations. Tenant isolation is FORCE RLS plus a workspace-bound policy per table, NOT ownership — a per-unit ext_<name> owner role exists only in the pre-merge migration gate (issue #628). The core owns public; nothing here is core data.';
