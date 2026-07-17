-- 0085: the deep-read dossier — one row per async crawl of an
-- organization's website. Created queued at enqueue, advanced by the
-- worker (queued → running → done|partial|failed), polled by the SPA.
-- The pages/skipped arrays are the crawl's own report ([{url, kind}] /
-- [{url, reason}]); proposal_ids links the approvals the read staged.
CREATE TABLE site_read (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id    uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  organization_id uuid NOT NULL,
  seed_url        text NOT NULL,
  status          text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','running','done','partial','failed')),
  pages           jsonb NOT NULL DEFAULT '[]',
  skipped         jsonb NOT NULL DEFAULT '[]',
  stopped_reason  text NULL CHECK (stopped_reason IS NULL OR stopped_reason IN ('budget','page_cap','byte_cap','deadline')),
  fact_count      integer NOT NULL DEFAULT 0,
  proposal_ids    uuid[] NOT NULL DEFAULT '{}',
  requested_by    text NOT NULL,
  started_at      timestamptz NULL,
  finished_at     timestamptz NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT site_read_org_fkey FOREIGN KEY (workspace_id, organization_id)
    REFERENCES organization (workspace_id, id) ON DELETE CASCADE
);

CREATE INDEX idx_site_read_org ON site_read (workspace_id, organization_id, created_at DESC);

-- At most one IN-FLIGHT read per organization: re-clicking "read the
-- site" joins the running read instead of racing a second crawl.
-- At most one in-flight read per (organization, seed_url): re-requesting the
-- SAME url joins the running read (idempotent), while a different url override
-- is its own read — it must actually read the url the caller named, never
-- silently join a crawl of a different page.
CREATE UNIQUE INDEX uq_site_read_inflight ON site_read (workspace_id, organization_id, seed_url)
  WHERE status IN ('queued','running');

ALTER TABLE site_read ENABLE ROW LEVEL SECURITY;
ALTER TABLE site_read FORCE ROW LEVEL SECURITY;
CREATE POLICY site_read_tenant_isolation ON site_read
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
