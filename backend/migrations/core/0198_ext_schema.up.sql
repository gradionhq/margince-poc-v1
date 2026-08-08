-- 0198: the ext schema — where every extension's own tables live (ADR-0069).
--
-- A separate schema, not more tables in public, is what makes the namespace
-- wall enforceable by Postgres rather than by convention: the per-unit
-- ext_<name> role gets CREATE, USAGE on ext ONLY and no grants on public, so
-- a unit's migration physically cannot touch a core relation, and the
-- catalog gate can enumerate ext's contents to see exactly what a unit
-- created. Inside ext, tables stay prefixed ext_<name>_<table> so two units
-- sharing the schema still cannot collide or address each other's data.
--
-- The core keeps owning public: this migration adds a namespace, it does not
-- move anything. The x_ COLUMN namespace on core tables is unrelated and
-- untouched.
CREATE SCHEMA IF NOT EXISTS ext;

-- USAGE only. The application role reads and writes extension tables through
-- grants each unit's own migration issues; it never creates objects here,
-- and it is not the owner, so an extension's DDL stays the extension role's
-- to perform and the app's compromise does not become schema authority.
GRANT USAGE ON SCHEMA ext TO margince_app;

COMMENT ON SCHEMA ext IS
  'Extension-owned tables (ADR-0069): ext_<name>_<table>, created and owned by the per-unit ext_<name> role. The core owns public; nothing here is core data.';
