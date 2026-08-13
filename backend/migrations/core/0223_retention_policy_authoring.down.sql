-- Reverses the constraint half only.
--
-- The RBAC backfill is deliberately NOT undone, on the 0179 precedent: reversing
-- the schema does not remove `retention_policy` from the application's RBAC
-- vocabulary (policy.coreObjects carries it in code), so deleting the grant
-- could not restore an earlier correct state — it could only recreate the
-- permanent 403 the backfill exists to fix, and it would do so on workspaces
-- that already held the grant, where the up's only-if-absent guard wrote nothing
-- and left no trace distinguishing them. A forward-only data repair has no
-- meaningful inverse.
--
-- Restoring the plain UNIQUE does have one: it widens what the table accepts, so
-- nothing stored under the stricter constraint can violate it.
ALTER TABLE retention_policy
  DROP CONSTRAINT retention_policy_unique,
  ADD  CONSTRAINT retention_policy_unique
       UNIQUE (workspace_id, object_type, category);
