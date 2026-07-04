-- Row-level security on every tenant table (data-model §1.3, ADR-0018):
-- ENABLE + FORCE (ENABLE-only "looks secure and is not" — FORCE binds the
-- table owner too), one tenant-isolation policy per table with
-- deny-on-unset semantics: a connection with no app.workspace_id GUC sees
-- ZERO rows and writes nothing (NULLIF turns '' into NULL; NULL = uuid is
-- never true). The Go transaction helper issues SET LOCAL app.workspace_id
-- per transaction; session-level SETs are banned (pool-leak path).
--
-- workspace itself and global reference/infra tables (event_outbox) stay
-- outside RLS per §1.2; audit_log and session/passport ARE tenant tables.

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
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format(
      'CREATE POLICY %I ON %I '
      || 'USING (workspace_id = NULLIF(current_setting(''app.workspace_id'', true), '''')::uuid) '
      || 'WITH CHECK (workspace_id = NULLIF(current_setting(''app.workspace_id'', true), '''')::uuid)',
      t || '_tenant_isolation', t);
  END LOOP;
END $$;
