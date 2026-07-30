-- Backfill the `channel_connection` RBAC object into the seeded system-role
-- policy documents of EXISTING workspaces (new workspaces get it from the
-- code-side seed, identity/internal/policy). Shipping a new first-class
-- object without this backfill is how it "works on a fresh database and
-- 403s everywhere else".
--
-- Posture mirrors `overlay_connection`, because a channel connection IS
-- workspace-wide config and not a record people work: one bot binding carries
-- every seat's inbound channel traffic, so create/update/delete are
-- admin/ops-only, and every other role reads the binding's status (a rep needs
-- to know whether the channel is live before expecting a reply to arrive there).

UPDATE role SET permissions = jsonb_set(
  permissions, '{objects,channel_connection}',
  '{"create":true,"read":true,"update":true,"delete":true}'::jsonb)
WHERE is_system AND key IN ('admin','ops')
  AND NOT permissions->'objects' ? 'channel_connection';

UPDATE role SET permissions = jsonb_set(
  permissions, '{objects,channel_connection}',
  '{"create":false,"read":true,"update":false,"delete":false}'::jsonb)
WHERE is_system AND key IN ('manager','rep','read_only')
  AND NOT permissions->'objects' ? 'channel_connection';
