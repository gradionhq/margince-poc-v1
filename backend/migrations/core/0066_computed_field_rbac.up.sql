-- 0066: backfill the `computed_field` RBAC object into the seeded
-- system-role policy documents of EXISTING workspaces (new workspaces get
-- it from the code-side seed, identity/internal/policy). Posture: unlike
-- custom_field (admin/ops-owned CRUD), computed_field is read-only for
-- EVERY role, admin/ops included — RD-AC-7: no runtime formula-authoring
-- surface exists, so there is no write to grant.

UPDATE role SET permissions = jsonb_set(
  permissions, '{objects,computed_field}',
  '{"create":false,"read":true,"update":false,"delete":false}'::jsonb)
WHERE is_system AND key IN ('admin','ops','manager','rep','read_only')
  AND NOT permissions->'objects' ? 'computed_field';
