-- 0114: backfill the `embedding_reindex` RBAC object into the seeded
-- system-role policy documents of EXISTING workspaces (new workspaces get
-- it from the code-side seed, identity/internal/policy). Posture: there is
-- no create/delete surface — it's a single deployment-level trigger, not a
-- record kind — so only read and update are ever granted. Triggering a
-- reindex is admin/ops-owned (the confirm route itself carries the
-- x-agent-access: human-only gate in the contract), while every role may
-- read it so any user's UI can show the "reindex needed" banner.

UPDATE role SET permissions = jsonb_set(
  permissions, '{objects,embedding_reindex}',
  '{"create":false,"read":true,"update":true,"delete":false}'::jsonb)
WHERE is_system AND key IN ('admin','ops')
  AND NOT permissions->'objects' ? 'embedding_reindex';
UPDATE role SET permissions = jsonb_set(
  permissions, '{objects,embedding_reindex}',
  '{"create":false,"read":true,"update":false,"delete":false}'::jsonb)
WHERE is_system AND key IN ('manager','rep','read_only')
  AND NOT permissions->'objects' ? 'embedding_reindex';
