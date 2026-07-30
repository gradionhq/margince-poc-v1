-- Per-user dismissals of the company view's next-step suggestions.
--
-- Keyed on the suggestion's EVIDENCE fingerprint rather than on its kind, so
-- a dismissal silences the situation the rep actually judged and re-arms by
-- itself when the evidence changes. A stalled deal that moves and stalls
-- again is a new fact about the account; a kind-keyed dismissal would bury it
-- forever and make the surface less useful the longer it ran.
--
-- Per USER, because advice one rep has judged is not advice their colleague
-- has seen. Like user_record_view (0142) and org_brief (0143) this is view
-- state, not a record fact: it carries no audit row and no outbox event.

CREATE TABLE suggestion_dismissal (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id    uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  user_id         uuid NOT NULL,
  organization_id uuid NOT NULL,
  -- The digest the read served, unchanged. Pinning the shape in the schema is
  -- what keeps this from being a write-anything store: the endpoint cannot
  -- re-derive a fingerprint (the situation may have moved on between the render
  -- and the click), so the shape is the check that stays true.
  fingerprint     text NOT NULL CONSTRAINT suggestion_dismissal_fingerprint_shape
                    CHECK (fingerprint ~ '^[0-9a-f]{64}$'),
  dismissed_at    timestamptz NOT NULL,
  UNIQUE (workspace_id, user_id, organization_id, fingerprint),
  -- Composite references: a dismissal can only name a user and an account of
  -- its own workspace, and it goes when either does.
  CONSTRAINT suggestion_dismissal_user_id_fkey FOREIGN KEY (workspace_id, user_id)
    REFERENCES app_user (workspace_id, id) ON DELETE CASCADE,
  CONSTRAINT suggestion_dismissal_org_fkey FOREIGN KEY (workspace_id, organization_id)
    REFERENCES organization (workspace_id, id) ON DELETE CASCADE
);

ALTER TABLE suggestion_dismissal ENABLE ROW LEVEL SECURITY;
ALTER TABLE suggestion_dismissal FORCE ROW LEVEL SECURITY;
CREATE POLICY suggestion_dismissal_tenant_isolation ON suggestion_dismissal
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

-- The org FK cascades on delete, and Art. 17 erasure deletes accounts.
CREATE INDEX suggestion_dismissal_organization_ix
  ON suggestion_dismissal (workspace_id, organization_id);
