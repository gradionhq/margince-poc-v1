-- The internal-domain gate's table (ADR-0063): mail whose counterparty is on
-- one of these domains is colleagues, not customers — the auto-create
-- pipeline skips it. Auto-seeded from the connecting user's own mail domain
-- (unless free-mail); admin CRUD is a follow-up, the table is the seam.

CREATE TABLE workspace_email_domain (
  workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  domain       text NOT NULL CHECK (domain = lower(domain) AND domain <> ''),
  created_at   timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (workspace_id, domain)
);

ALTER TABLE workspace_email_domain ENABLE ROW LEVEL SECURITY;
ALTER TABLE workspace_email_domain FORCE ROW LEVEL SECURITY;
CREATE POLICY workspace_email_domain_tenant_isolation ON workspace_email_domain
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
