DO $$
DECLARE
  t text;
  tenant_tables text[] := ARRAY[
    'app_user', 'team', 'team_membership', 'role', 'role_assignment',
    'session', 'passport',
    'person', 'person_email', 'person_phone',
    'consent_purpose', 'person_consent', 'consent_event', 'retention_policy',
    'organization', 'organization_domain', 'partner',
    'relationship',
    'pipeline', 'stage', 'deal', 'deal_stage_history', 'fx_rate',
    'activity', 'activity_link',
    'lead',
    'list', 'list_member', 'tag', 'taggable', 'attachment', 'record_grant',
    'audit_log'
  ];
BEGIN
  FOREACH t IN ARRAY tenant_tables LOOP
    EXECUTE format('DROP POLICY IF EXISTS %I ON %I', t || '_tenant_isolation', t);
    EXECUTE format('ALTER TABLE %I NO FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I DISABLE ROW LEVEL SECURITY', t);
  END LOOP;
END $$;
