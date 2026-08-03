-- 0168: the workspace's own consumer-mail list (CAP-PARAM-5).
--
-- The shipped baseline is a third-party dataset of some 8 700 domains. A list
-- that size is right far more often than a hand-typed one and still wrong
-- sometimes, in both directions: it misses a regional provider, or it claims a
-- domain an operator's real customers mail from. Neither error can wait for a
-- release, and both are answerable by the people who read the mail.
--
-- 'extra' adds a domain the baseline missed. 'never' takes one back out and
-- wins over everything, because an operator locked out by the baseline has no
-- other way in.
--
-- Workspace-shared, admin-curated: whether a domain can name a company is a
-- statement about the domain, not about whoever happens to be reading the mail.
-- This is the surviving domain control after the per-user personal-mail
-- exclusion rules were withdrawn (0165).

CREATE TABLE capture_freemail_domain (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  -- The registrable domain (eSLD), lowercased: one entry covers every
  -- subdomain, the same way the matcher reads the baseline.
  domain text NOT NULL CHECK (domain = lower(domain) AND domain <> ''),
  kind   text NOT NULL CHECK (kind IN ('extra', 'never')),

  created_at timestamptz NOT NULL DEFAULT now(),
  -- Who added it. The list is workspace-shared, so this is attribution for the
  -- audit trail, never a scope.
  created_by uuid NULL,
  -- SET NULL names its COLUMN, or an unqualified action would null the
  -- NOT NULL workspace_id and make deleting a user fail outright.
  CONSTRAINT capture_freemail_domain_created_by_fkey
    FOREIGN KEY (workspace_id, created_by) REFERENCES app_user (workspace_id, id)
    ON DELETE SET NULL (created_by)
);

-- One verdict per domain: a domain cannot be both added and carved out, and
-- re-adding an existing one is a no-op rather than a second row. Delete is a
-- hard removal (there is nothing to retain about a withdrawn list entry), so a
-- plain unique is correct here.
CREATE UNIQUE INDEX uq_capture_freemail_domain
  ON capture_freemail_domain (workspace_id, domain);

ALTER TABLE capture_freemail_domain ENABLE ROW LEVEL SECURITY;
ALTER TABLE capture_freemail_domain FORCE ROW LEVEL SECURITY;
CREATE POLICY capture_freemail_domain_tenant_isolation ON capture_freemail_domain
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
