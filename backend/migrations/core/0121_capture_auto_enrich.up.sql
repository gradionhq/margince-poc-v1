-- ADR-0072/A118 (CAP-PARAM-7): the captured-organization auto-enrich setting
-- and its RBAC object.
--
-- The setting is workspace-shared config (a singleton per installation): when
-- ON, every surviving auto-created company gets a governed deep-read under a
-- daily spend cap. Default ON is the TESTING posture pinned in the ADR; the GA
-- default is a later decision. Not fork `x_` — ratified core.
ALTER TABLE workspace
  ADD COLUMN capture_auto_enrich boolean NOT NULL DEFAULT true;

-- Backfill the `capture_settings` RBAC object into EXISTING workspaces' seeded
-- system-role policy documents (new workspaces get it from the code-side seed,
-- identity/internal/policy). Posture: everyone READS the toggle (a rep needs to
-- see whether auto-enrich is live, like a quota's attainment read), only
-- admin/ops UPDATE it. No create/delete — it is a singleton workspace setting,
-- not a record kind — so both are FALSE for every role, closing the grant
-- against any future generic create/delete path. The PATCH route also carries
-- x-agent-access: human-only.
UPDATE role SET permissions = jsonb_set(
  permissions, '{objects,capture_settings}',
  '{"create":false,"read":true,"update":true,"delete":false}'::jsonb)
WHERE is_system AND key IN ('admin', 'ops')
  AND NOT permissions->'objects' ? 'capture_settings';

UPDATE role SET permissions = jsonb_set(
  permissions, '{objects,capture_settings}',
  '{"create":false,"read":true,"update":false,"delete":false}'::jsonb)
WHERE is_system AND key IN ('manager', 'rep', 'read_only')
  AND NOT permissions->'objects' ? 'capture_settings';
