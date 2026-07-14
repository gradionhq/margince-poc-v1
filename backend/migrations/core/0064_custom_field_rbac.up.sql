-- 0064: backfill the `custom_field` RBAC object into the seeded
-- system-role policy documents of EXISTING workspaces (new workspaces get
-- it from the code-side seed, identity/internal/policy). Posture: the
-- pipeline-config precedent — a field definition
-- reshapes what the system stores for everyone's records, so changing the
-- catalog is admin/ops-owned while every role may read it (the admin
-- field table and record payload projections both need the catalog).

UPDATE role SET permissions = jsonb_set(
  permissions, '{objects,custom_field}',
  '{"create":true,"read":true,"update":true,"delete":true}'::jsonb)
WHERE is_system AND key IN ('admin','ops')
  AND NOT permissions->'objects' ? 'custom_field';
UPDATE role SET permissions = jsonb_set(
  permissions, '{objects,custom_field}',
  '{"create":false,"read":true,"update":false,"delete":false}'::jsonb)
WHERE is_system AND key IN ('manager','rep','read_only')
  AND NOT permissions->'objects' ? 'custom_field';
