-- 0072: backfill the `offer_template` RBAC object into the seeded
-- system-role policy documents of EXISTING workspaces (new workspaces get
-- it from the code-side seed, identity/internal/policy). Posture mirrors
-- product/offer, NOT the pipeline-config posture (custom_field/quota):
-- a template is the offer's own branding input, not a locked-down schema
-- surface, so reps create and work templates like any other offer-adjacent
-- record; delete stays manager/admin/ops. Grants below match
-- policy.go's defaults map exactly (offerTemplate: admin/manager/ops crud,
-- rep create+read+update, read_only read).

UPDATE role SET permissions = jsonb_set(
  permissions, '{objects,offer_template}',
  '{"create":true,"read":true,"update":true,"delete":true}'::jsonb)
WHERE is_system AND key IN ('admin','ops','manager')
  AND NOT permissions->'objects' ? 'offer_template';
UPDATE role SET permissions = jsonb_set(
  permissions, '{objects,offer_template}',
  '{"create":true,"read":true,"update":true,"delete":false}'::jsonb)
WHERE is_system AND key = 'rep'
  AND NOT permissions->'objects' ? 'offer_template';
UPDATE role SET permissions = jsonb_set(
  permissions, '{objects,offer_template}',
  '{"create":false,"read":true,"update":false,"delete":false}'::jsonb)
WHERE is_system AND key = 'read_only'
  AND NOT permissions->'objects' ? 'offer_template';
