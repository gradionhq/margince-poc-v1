-- 0036: public booking pages (feedback/14 — B-EP09.14). One row per
-- host's anonymous booking URL: an unguessable slug that resolves to
-- (workspace, host) BEFORE any session or workspace header exists, so —
-- like `workspace` itself (data-model §1.2) — this table is DELIBERATELY
-- not under RLS: it is the resolver the tenant GUC comes from, and it
-- carries no CRM record data (slug, workspace, host, revocation only).
-- The slug is a public identifier, not a credential: stored plaintext.
CREATE TABLE booking_page (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id  uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  host_user_id  uuid NOT NULL,
  slug          text NOT NULL UNIQUE,
  created_at    timestamptz NOT NULL DEFAULT now(),
  revoked_at    timestamptz NULL,
  CONSTRAINT booking_page_host_fkey FOREIGN KEY (workspace_id, host_user_id)
    REFERENCES app_user (workspace_id, id) ON DELETE CASCADE
);
CREATE INDEX idx_booking_page_host ON booking_page (workspace_id, host_user_id) WHERE revoked_at IS NULL;
