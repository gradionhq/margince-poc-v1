-- 0217: retention policies become authorable — the constraint that makes one
-- row per scope true, and the RBAC object that gates authoring one.
--
-- Two halves, and they are one change: the surface that lets an admin add a
-- policy row is exactly what makes the missing uniqueness reachable.

-- Half one — the UNIQUE constraint enforces what two documents already claim.
--
-- retention_policy_unique spans a NULLABLE category (DM-SEED-2 is
-- ('activity', NULL)), and Postgres counts NULLs as distinct: any number of
-- ('activity', NULL) rows are legal today. Nothing noticed because nothing but
-- the bootstrap ever wrote the table. Two places assert otherwise:
--
--   * data-model §3.4 / UC-GDPR-09 step 2 — "a second row for the same
--     (object_type, category) scope is rejected", the property the ladder's
--     "separate rows at increasing retain_days" reading rests on.
--   * privacy.MaxPassDuration — derived from the SCOPE COUNT, times the batch
--     bound, times the per-record allowance. A duplicate scope claims its own
--     full batch, so the scheduler's declared timeout becomes a fiction rather
--     than a cap.
--
-- NULLS NOT DISTINCT (PG15+) is the fix. The constraint NAME is kept so
-- ADR-0091 step 5 finds it when it collapses the composite keys, and so
-- storekit.UniqueViolation can key the 409 on it.
--
-- No repair pass is owed: the bootstrap plants one row per scope and is the
-- only writer that has ever existed, so no database can hold a duplicate. If
-- one somehow does, this fails loudly here rather than leaving the bound a lie.
ALTER TABLE retention_policy
  DROP CONSTRAINT retention_policy_unique,
  ADD  CONSTRAINT retention_policy_unique
       UNIQUE NULLS NOT DISTINCT (workspace_id, object_type, category);

-- Half two — backfill the `retention_policy` RBAC object into the seeded
-- system-role documents of EXISTING workspaces. New workspaces get it from the
-- code-side seed (identity/internal/policy); the bootstrap seed writes its
-- defaults once and never re-syncs, so without this every installation that
-- predates the release 403s on the retention screen permanently.
--
-- Posture: admin/ops-only on EVERY verb, read included — the import_run and
-- embedding_reindex precedent rather than quota's. A retention policy decides
-- what the installation destroys and when; the screen that shows it is an admin
-- surface, and manager/rep/read_only have no consumer for the read the way a
-- rep legitimately reads a quota's attainment or an overlay connection's
-- status. The zero grant is the honest answer, not an oversight.
DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,retention_policy}',
      '{"create":true,"read":true,"update":true,"delete":true}'::jsonb)
    WHERE (is_system AND key IN ('admin','ops')
      AND NOT permissions->'objects' ? 'retention_policy')
      AND role.workspace_id = ws;

    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,retention_policy}',
      '{"create":false,"read":false,"update":false,"delete":false}'::jsonb)
    WHERE (is_system AND key IN ('manager','rep','read_only')
      AND NOT permissions->'objects' ? 'retention_policy')
      AND role.workspace_id = ws;
  END LOOP;
END $$;
