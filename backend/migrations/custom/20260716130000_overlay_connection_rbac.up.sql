-- 20260716130000: backfill the `overlay_connection` RBAC object into the
-- seeded system-role policy documents of EXISTING workspaces (new
-- workspaces get it from the code-side seed, identity/internal/policy).
-- Fork-owned migration (ADR-0017 custom namespace) because
-- overlay_connection is the overlay module's own object, not upstream's.
--
-- Posture mirrors the quota RBAC backfill precedent
-- (migrations/core/0068_quota_rbac.up.sql): connecting/disconnecting the
-- workspace's incumbent binding is destructive workspace-wide config (it
-- purges the mirror and flips sor_mode for everyone), so
-- create/update/delete are admin/ops-only; every role may read the
-- connection status.

UPDATE role SET permissions = jsonb_set(
  permissions, '{objects,overlay_connection}',
  '{"create":true,"read":true,"update":true,"delete":true}'::jsonb)
WHERE is_system AND key IN ('admin','ops')
  AND NOT permissions->'objects' ? 'overlay_connection';
UPDATE role SET permissions = jsonb_set(
  permissions, '{objects,overlay_connection}',
  '{"create":false,"read":true,"update":false,"delete":false}'::jsonb)
WHERE is_system AND key IN ('manager','rep','read_only')
  AND NOT permissions->'objects' ? 'overlay_connection';
