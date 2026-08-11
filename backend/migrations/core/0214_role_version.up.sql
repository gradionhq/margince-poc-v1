-- A role becomes an optimistically-locked row (data-model §1.3a).
--
-- `role.permissions` had exactly one writer until now — a hand-written UPDATE by
-- whoever had a psql prompt — so nothing raced it and no version was owed. The
-- role grant editor (setRoleObjectGrant) makes it a client-driven edit, and the
-- document is ONE jsonb value: two admins on the same screen editing two
-- DIFFERENT objects are not a merge conflict the database can see, they are a
-- lost write. One admin revokes `delete` on an object while another, from a read
-- taken a moment earlier, grants `create` on some other object; the second write
-- carries the whole document and the revoke disappears with nothing to say it
-- ever happened. That is the wrong failure for a permission model.
--
-- ADD COLUMN with a DEFAULT, not a backfill: PostgreSQL 11+ stores the default
-- in the catalog rather than rewriting the table, and the role table holds a
-- handful of rows per workspace either way.
ALTER TABLE role ADD COLUMN version bigint NOT NULL DEFAULT 1;

-- The trigger moves from set_updated_at to the version-bumping variant. Both set
-- updated_at; only the second increments version, and it must be the one
-- attached or every write would leave the version where it was and every
-- If-Match would pass forever — a concurrency guard that is present and inert is
-- worse than an absent one, because it reads as protection.
--
-- Dropping and recreating rather than CREATE OR REPLACE TRIGGER: that syntax
-- arrived in PostgreSQL 14 and the rest of this tree's triggers are written the
-- portable way.
DROP TRIGGER IF EXISTS trg_role_updated ON role;
CREATE TRIGGER trg_role_updated BEFORE UPDATE ON role
  FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();
