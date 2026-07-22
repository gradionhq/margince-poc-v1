-- 0114: backfill the `embedding_reindex` RBAC object into the seeded
-- system-role policy documents of EXISTING workspaces (new workspaces get
-- it from the code-side seed, identity/internal/policy). Posture: there is
-- no create/delete surface — it's a single deployment-level trigger, not a
-- record kind — so only read and update are ever granted, and both are
-- admin/ops-only. Triggering a reindex is admin/ops-owned (the confirm
-- route itself carries the x-agent-access: human-only gate in the
-- contract); the read is admin/ops-only too — the banner/card that
-- consumes it is itself ops-gated in the SPA, so manager/rep/read_only
-- have no legitimate consumer and get no grant at all.

UPDATE role SET permissions = jsonb_set(
  permissions, '{objects,embedding_reindex}',
  '{"create":false,"read":true,"update":true,"delete":false}'::jsonb)
WHERE is_system AND key IN ('admin','ops')
  AND NOT permissions->'objects' ? 'embedding_reindex';
