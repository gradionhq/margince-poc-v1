-- Backfill the `list` and `tag` RBAC objects into the seeded system-role policy
-- documents of EXISTING workspaces.
--
-- Same defect as 0179-0182, found by reviewing the gate those added: `list` and
-- `tag` entered policy.coreObjects in the second commit of the project's first
-- day, after the initial six (person, organization, deal, lead, activity,
-- pipeline) and before the backfill practice began at 0035 — and no migration
-- ever granted them. Any workspace bootstrapped in that window holds no list or
-- tag grant and 403s on both surfaces permanently.
--
-- The window is narrow, so on most installations the only-if-absent guard makes
-- this a no-op. It is written anyway: "probably nobody is affected" is not a
-- property this codebase can assert about a customer's database, and the guard
-- makes the write free where it is not needed.
--
-- Posture: lists and tags are everyday organizational surfaces — admin, ops and
-- manager work them fully; a rep creates and uses them but archiving stays with
-- manager and above; read_only reads.

UPDATE role SET permissions = jsonb_set(
  permissions, '{objects,list}',
  '{"create":true,"read":true,"update":true,"delete":true}'::jsonb)
WHERE is_system AND key IN ('admin','ops','manager')
  AND NOT permissions->'objects' ? 'list';

UPDATE role SET permissions = jsonb_set(
  permissions, '{objects,list}',
  '{"create":true,"read":true,"update":true,"delete":false}'::jsonb)
WHERE is_system AND key = 'rep'
  AND NOT permissions->'objects' ? 'list';

UPDATE role SET permissions = jsonb_set(
  permissions, '{objects,list}',
  '{"create":false,"read":true,"update":false,"delete":false}'::jsonb)
WHERE is_system AND key = 'read_only'
  AND NOT permissions->'objects' ? 'list';

UPDATE role SET permissions = jsonb_set(
  permissions, '{objects,tag}',
  '{"create":true,"read":true,"update":true,"delete":true}'::jsonb)
WHERE is_system AND key IN ('admin','ops','manager')
  AND NOT permissions->'objects' ? 'tag';

UPDATE role SET permissions = jsonb_set(
  permissions, '{objects,tag}',
  '{"create":true,"read":true,"update":true,"delete":false}'::jsonb)
WHERE is_system AND key = 'rep'
  AND NOT permissions->'objects' ? 'tag';

UPDATE role SET permissions = jsonb_set(
  permissions, '{objects,tag}',
  '{"create":false,"read":true,"update":false,"delete":false}'::jsonb)
WHERE is_system AND key = 'read_only'
  AND NOT permissions->'objects' ? 'tag';
