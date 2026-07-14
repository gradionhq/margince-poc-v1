-- 0068: backfill the `quota` RBAC object into the seeded system-role policy
-- documents of EXISTING workspaces (new workspaces get it from the code-side
-- seed, identity/internal/policy). Posture mirrors the pipeline-config
-- precedent (as in 0064's custom_field backfill): a sales
-- target reshapes what the system measures reps/teams against, so
-- creating/changing/archiving a quota is admin/ops-owned while every role
-- may read it (a rep needs to see their own attainment against it).

UPDATE role SET permissions = jsonb_set(
  permissions, '{objects,quota}',
  '{"create":true,"read":true,"update":true,"delete":true}'::jsonb)
WHERE is_system AND key IN ('admin','ops')
  AND NOT permissions->'objects' ? 'quota';
UPDATE role SET permissions = jsonb_set(
  permissions, '{objects,quota}',
  '{"create":false,"read":true,"update":false,"delete":false}'::jsonb)
WHERE is_system AND key IN ('manager','rep','read_only')
  AND NOT permissions->'objects' ? 'quota';
