-- Backfill the `webhook_subscription` RBAC object into the seeded system-role
-- policy documents of EXISTING workspaces. It entered the code-side seed
-- (identity/internal/policy) without a backfill, so a workspace bootstrapped
-- before that release holds no grant and 403s on every subscription route.
--
-- Posture mirrors the admin/ops-owned integration surfaces: a subscription
-- registers outbound egress of governed events, so managing the fan-out is
-- workspace integration config (create/update/delete admin/ops-only), while
-- every role may read subscriptions and their delivery health.
--
-- (UC-E10-04 narrates a Rep registering one. That posture question is tracked
-- upstream against the spec, not settled here — this backfill reproduces the
-- code-side seed exactly, which is all a backfill may do.)
DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,webhook_subscription}',
      '{"create":true,"read":true,"update":true,"delete":true}'::jsonb)
    WHERE (is_system AND key IN ('admin','ops')
      AND NOT permissions->'objects' ? 'webhook_subscription')
      AND role.workspace_id = ws;

    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,webhook_subscription}',
      '{"create":false,"read":true,"update":false,"delete":false}'::jsonb)
    WHERE (is_system AND key IN ('manager','rep','read_only')
      AND NOT permissions->'objects' ? 'webhook_subscription')
      AND role.workspace_id = ws;
  END LOOP;
END $$;
