-- Backfill the `relationship` RBAC object into the seeded system-role policy
-- documents of EXISTING workspaces. It entered the code-side seed
-- (identity/internal/policy) without a backfill, so a workspace bootstrapped
-- before that release holds no grant and 403s on every relationship route.
--
-- Posture follows the record tiers: admin/ops/manager work relationships
-- fully; a rep creates and maintains them but never deletes (the house rule
-- for records a rep works — deletion stays with manager and above);
-- read_only reads.

UPDATE role SET permissions = jsonb_set(
  permissions, '{objects,relationship}',
  '{"create":true,"read":true,"update":true,"delete":true}'::jsonb)
WHERE is_system AND key IN ('admin','ops','manager')
  AND NOT permissions->'objects' ? 'relationship';

UPDATE role SET permissions = jsonb_set(
  permissions, '{objects,relationship}',
  '{"create":true,"read":true,"update":true,"delete":false}'::jsonb)
WHERE is_system AND key = 'rep'
  AND NOT permissions->'objects' ? 'relationship';

UPDATE role SET permissions = jsonb_set(
  permissions, '{objects,relationship}',
  '{"create":false,"read":true,"update":false,"delete":false}'::jsonb)
WHERE is_system AND key = 'read_only'
  AND NOT permissions->'objects' ? 'relationship';
