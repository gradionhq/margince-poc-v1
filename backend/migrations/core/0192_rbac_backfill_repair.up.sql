-- Re-apply every core RBAC backfill that row-level security may have discarded,
-- for databases that already recorded the migration carrying it.
--
-- An applied version never runs again, so correcting those migrations in place
-- fixes only installations migrated from scratch afterwards. This migration is
-- what reaches the ones already deployed.
--
-- Scope is every guarded backfill in the core namespace, not the subset observed
-- to have been lost. Which ones an installation actually lost depends on where in
-- this sequence it first booted: roles are seeded by app code at bootstrap, so a
-- backfill that ran before an installation existed had nothing to write, and one
-- that ran after it did. No migration can know that boot point for every
-- installation, and guessing it wrong leaves a permanent 403 nobody can see is
-- missing. Every block below is guarded on key ABSENCE, exactly as its original
-- was, so re-applying one that already landed writes nothing -- covering all of
-- them costs a no-op and removes the need to know.
--
-- The bodies are the originals: same objects, same roles, same payloads.
-- TestTheRepairsCoverEveryGuardedRBACBackfill derives both sides from the tree
-- and fails if they ever disagree.
--
-- The RBAC losses are the ones this repairs. Whether a non-RBAC backfill was also
-- lost depends on whether an installation held matching rows when it ran, which
-- no migration can answer for every installation -- issue #541 tracks the audit.
DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);

    -- 0035_automations
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,automation}',
      '{"create":true,"read":true,"update":true,"delete":true}'::jsonb)
    WHERE (is_system AND key IN ('admin','ops')
      AND NOT permissions->'objects' ? 'automation')
      AND role.workspace_id = ws;
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,automation}',
      '{"create":false,"read":true,"update":false,"delete":false}'::jsonb)
    WHERE (is_system AND key IN ('manager','rep','read_only')
      AND NOT permissions->'objects' ? 'automation')
      AND role.workspace_id = ws;

    -- 0042_voice_profile
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,voice_profile}',
      '{"create":true,"read":true,"update":true,"delete":true}'::jsonb)
    WHERE (is_system AND key IN ('admin','ops','manager')
      AND NOT permissions->'objects' ? 'voice_profile')
      AND role.workspace_id = ws;
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,voice_profile}',
      '{"create":true,"read":true,"update":true,"delete":false}'::jsonb)
    WHERE (is_system AND key = 'rep'
      AND NOT permissions->'objects' ? 'voice_profile')
      AND role.workspace_id = ws;
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,voice_profile}',
      '{"create":false,"read":true,"update":false,"delete":false}'::jsonb)
    WHERE (is_system AND key = 'read_only'
      AND NOT permissions->'objects' ? 'voice_profile')
      AND role.workspace_id = ws;

    -- 0043_products
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,product}',
      '{"create":true,"read":true,"update":true,"delete":true}'::jsonb)
    WHERE (is_system AND key IN ('admin','ops','manager')
      AND NOT permissions->'objects' ? 'product')
      AND role.workspace_id = ws;
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,product}',
      '{"create":true,"read":true,"update":true,"delete":false}'::jsonb)
    WHERE (is_system AND key = 'rep'
      AND NOT permissions->'objects' ? 'product')
      AND role.workspace_id = ws;
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,product}',
      '{"create":false,"read":true,"update":false,"delete":false}'::jsonb)
    WHERE (is_system AND key = 'read_only'
      AND NOT permissions->'objects' ? 'product')
      AND role.workspace_id = ws;

    -- 0044_offers
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,offer}',
      '{"create":true,"read":true,"update":true,"delete":true}'::jsonb)
    WHERE (is_system AND key IN ('admin','ops','manager')
      AND NOT permissions->'objects' ? 'offer')
      AND role.workspace_id = ws;
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,offer}',
      '{"create":true,"read":true,"update":true,"delete":false}'::jsonb)
    WHERE (is_system AND key = 'rep'
      AND NOT permissions->'objects' ? 'offer')
      AND role.workspace_id = ws;
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,offer}',
      '{"create":false,"read":true,"update":false,"delete":false}'::jsonb)
    WHERE (is_system AND key = 'read_only'
      AND NOT permissions->'objects' ? 'offer')
      AND role.workspace_id = ws;

    -- 0047_signals
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,signal}',
      '{"create":true,"read":true,"update":true,"delete":true}'::jsonb)
    WHERE (is_system AND key IN ('admin','ops','manager')
      AND NOT permissions->'objects' ? 'signal')
      AND role.workspace_id = ws;
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,signal}',
      '{"create":true,"read":true,"update":true,"delete":false}'::jsonb)
    WHERE (is_system AND key = 'rep'
      AND NOT permissions->'objects' ? 'signal')
      AND role.workspace_id = ws;
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,signal}',
      '{"create":false,"read":true,"update":false,"delete":false}'::jsonb)
    WHERE (is_system AND key = 'read_only'
      AND NOT permissions->'objects' ? 'signal')
      AND role.workspace_id = ws;

    -- 0064_custom_field_rbac
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,custom_field}',
      '{"create":true,"read":true,"update":true,"delete":true}'::jsonb)
    WHERE (is_system AND key IN ('admin','ops')
      AND NOT permissions->'objects' ? 'custom_field')
      AND role.workspace_id = ws;
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,custom_field}',
      '{"create":false,"read":true,"update":false,"delete":false}'::jsonb)
    WHERE (is_system AND key IN ('manager','rep','read_only')
      AND NOT permissions->'objects' ? 'custom_field')
      AND role.workspace_id = ws;

    -- 0066_computed_field_rbac
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,computed_field}',
      '{"create":false,"read":true,"update":false,"delete":false}'::jsonb)
    WHERE (is_system AND key IN ('admin','ops','manager','rep','read_only')
      AND NOT permissions->'objects' ? 'computed_field')
      AND role.workspace_id = ws;

    -- 0068_quota_rbac
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,quota}',
      '{"create":true,"read":true,"update":true,"delete":true}'::jsonb)
    WHERE (is_system AND key IN ('admin','ops')
      AND NOT permissions->'objects' ? 'quota')
      AND role.workspace_id = ws;
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,quota}',
      '{"create":false,"read":true,"update":false,"delete":false}'::jsonb)
    WHERE (is_system AND key IN ('manager','rep','read_only')
      AND NOT permissions->'objects' ? 'quota')
      AND role.workspace_id = ws;

    -- 0072_offer_template_rbac
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,offer_template}',
      '{"create":true,"read":true,"update":true,"delete":true}'::jsonb)
    WHERE (is_system AND key IN ('admin','ops','manager')
      AND NOT permissions->'objects' ? 'offer_template')
      AND role.workspace_id = ws;
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,offer_template}',
      '{"create":true,"read":true,"update":true,"delete":false}'::jsonb)
    WHERE (is_system AND key = 'rep'
      AND NOT permissions->'objects' ? 'offer_template')
      AND role.workspace_id = ws;
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,offer_template}',
      '{"create":false,"read":true,"update":false,"delete":false}'::jsonb)
    WHERE (is_system AND key = 'read_only'
      AND NOT permissions->'objects' ? 'offer_template')
      AND role.workspace_id = ws;

    -- 0115_embedding_reindex_rbac
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,embedding_reindex}',
      '{"create":false,"read":true,"update":true,"delete":false}'::jsonb)
    WHERE (is_system AND key IN ('admin','ops')
      AND NOT permissions->'objects' ? 'embedding_reindex')
      AND role.workspace_id = ws;

    -- 0117_rate_rbac
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,fx_rate}',
      '{"create":true,"read":true,"update":true,"delete":false}'::jsonb)
    WHERE (is_system AND key IN ('admin','ops')
      AND NOT permissions->'objects' ? 'fx_rate')
      AND role.workspace_id = ws;
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,ai_model_rate}',
      '{"create":true,"read":true,"update":true,"delete":false}'::jsonb)
    WHERE (is_system AND key IN ('admin','ops')
      AND NOT permissions->'objects' ? 'ai_model_rate')
      AND role.workspace_id = ws;

    -- 0121_capture_auto_enrich
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,capture_settings}',
      '{"create":false,"read":true,"update":true,"delete":false}'::jsonb)
    WHERE (is_system AND key IN ('admin','ops')
      AND NOT permissions->'objects' ? 'capture_settings')
      AND role.workspace_id = ws;
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,capture_settings}',
      '{"create":false,"read":true,"update":false,"delete":false}'::jsonb)
    WHERE (is_system AND key IN ('manager','rep','read_only')
      AND NOT permissions->'objects' ? 'capture_settings')
      AND role.workspace_id = ws;

    -- 0132_project_rbac
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,project}',
      '{"create":true,"read":true,"update":true,"delete":true}'::jsonb)
    WHERE (is_system AND key IN ('admin','manager','ops')
      AND NOT permissions->'objects' ? 'project')
      AND role.workspace_id = ws;
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,project}',
      '{"create":true,"read":true,"update":true,"delete":false}'::jsonb)
    WHERE (is_system AND key = 'rep'
      AND NOT permissions->'objects' ? 'project')
      AND role.workspace_id = ws;
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,project}',
      '{"create":false,"read":true,"update":false,"delete":false}'::jsonb)
    WHERE (is_system AND key = 'read_only'
      AND NOT permissions->'objects' ? 'project')
      AND role.workspace_id = ws;

    -- 0154_channel_connection_rbac
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,channel_connection}',
      '{"create":true,"read":true,"update":true,"delete":true}'::jsonb)
    WHERE (is_system AND key IN ('admin','ops')
      AND NOT permissions->'objects' ? 'channel_connection')
      AND role.workspace_id = ws;
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,channel_connection}',
      '{"create":false,"read":true,"update":false,"delete":false}'::jsonb)
    WHERE (is_system AND key IN ('manager','rep','read_only')
      AND NOT permissions->'objects' ? 'channel_connection')
      AND role.workspace_id = ws;

    -- 0179_saved_view_rbac
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,saved_view}',
      '{"create":true,"read":true,"update":true,"delete":true}'::jsonb)
    WHERE (is_system AND key IN ('admin','ops','manager','rep','read_only')
      AND NOT permissions->'objects' ? 'saved_view')
      AND role.workspace_id = ws;

    -- 0180_webhook_subscription_rbac
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

    -- 0181_relationship_rbac
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,relationship}',
      '{"create":true,"read":true,"update":true,"delete":true}'::jsonb)
    WHERE (is_system AND key IN ('admin','ops','manager')
      AND NOT permissions->'objects' ? 'relationship')
      AND role.workspace_id = ws;
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,relationship}',
      '{"create":true,"read":true,"update":true,"delete":false}'::jsonb)
    WHERE (is_system AND key = 'rep'
      AND NOT permissions->'objects' ? 'relationship')
      AND role.workspace_id = ws;
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,relationship}',
      '{"create":false,"read":true,"update":false,"delete":false}'::jsonb)
    WHERE (is_system AND key = 'read_only'
      AND NOT permissions->'objects' ? 'relationship')
      AND role.workspace_id = ws;

    -- 0182_partner_rbac
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,partner}',
      '{"create":true,"read":true,"update":true,"delete":true}'::jsonb)
    WHERE (is_system AND key IN ('admin','ops','manager')
      AND NOT permissions->'objects' ? 'partner')
      AND role.workspace_id = ws;
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,partner}',
      '{"create":false,"read":true,"update":false,"delete":false}'::jsonb)
    WHERE (is_system AND key IN ('rep','read_only')
      AND NOT permissions->'objects' ? 'partner')
      AND role.workspace_id = ws;

    -- 0183_list_tag_rbac
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,list}',
      '{"create":true,"read":true,"update":true,"delete":true}'::jsonb)
    WHERE (is_system AND key IN ('admin','ops','manager')
      AND NOT permissions->'objects' ? 'list')
      AND role.workspace_id = ws;
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,list}',
      '{"create":true,"read":true,"update":true,"delete":false}'::jsonb)
    WHERE (is_system AND key = 'rep'
      AND NOT permissions->'objects' ? 'list')
      AND role.workspace_id = ws;
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,list}',
      '{"create":false,"read":true,"update":false,"delete":false}'::jsonb)
    WHERE (is_system AND key = 'read_only'
      AND NOT permissions->'objects' ? 'list')
      AND role.workspace_id = ws;
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,tag}',
      '{"create":true,"read":true,"update":true,"delete":true}'::jsonb)
    WHERE (is_system AND key IN ('admin','ops','manager')
      AND NOT permissions->'objects' ? 'tag')
      AND role.workspace_id = ws;
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,tag}',
      '{"create":true,"read":true,"update":true,"delete":false}'::jsonb)
    WHERE (is_system AND key = 'rep'
      AND NOT permissions->'objects' ? 'tag')
      AND role.workspace_id = ws;
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,tag}',
      '{"create":false,"read":true,"update":false,"delete":false}'::jsonb)
    WHERE (is_system AND key = 'read_only'
      AND NOT permissions->'objects' ? 'tag')
      AND role.workspace_id = ws;

    -- 0191_installation_settings
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,installation_settings}',
      '{"create":false,"read":true,"update":true,"delete":false}'::jsonb)
    WHERE (is_system AND key IN ('admin','ops')
      AND NOT permissions->'objects' ? 'installation_settings')
      AND role.workspace_id = ws;
    UPDATE role SET permissions = jsonb_set(
      permissions, '{objects,installation_settings}',
      '{"create":false,"read":true,"update":false,"delete":false}'::jsonb)
    WHERE (is_system AND key IN ('manager','rep','read_only')
      AND NOT permissions->'objects' ? 'installation_settings')
      AND role.workspace_id = ws;
  END LOOP;
END $$;
