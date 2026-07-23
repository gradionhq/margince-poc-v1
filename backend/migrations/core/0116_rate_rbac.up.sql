-- 0116: backfill the `fx_rate` + `ai_model_rate` RBAC objects into the seeded
-- system-role policy documents of EXISTING workspaces (new workspaces get them
-- from the code-side seed, identity/internal/policy). Posture mirrors
-- embedding_reindex (0115): an admin/ops-only config surface, view AND edit.
-- Editing a currency rate or a model price reshapes every money rollup
-- (attainment, org rollup, brief ranking, AI price-on-read), so it is
-- admin/ops-owned; the editor tab is itself org-gated in the SPA, so
-- manager/rep/read_only have no legitimate consumer and get no grant at all
-- (absence denies). The POST routes also carry x-agent-access: human-only.

UPDATE role SET permissions = jsonb_set(
  permissions, '{objects,fx_rate}',
  '{"create":true,"read":true,"update":true,"delete":true}'::jsonb)
WHERE is_system AND key IN ('admin','ops')
  AND NOT permissions->'objects' ? 'fx_rate';

UPDATE role SET permissions = jsonb_set(
  permissions, '{objects,ai_model_rate}',
  '{"create":true,"read":true,"update":true,"delete":true}'::jsonb)
WHERE is_system AND key IN ('admin','ops')
  AND NOT permissions->'objects' ? 'ai_model_rate';
