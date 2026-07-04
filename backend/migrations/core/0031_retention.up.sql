-- 0031: the erasure resurrection-suppression list (A13). An erased
-- subject lives on ONLY as identifier hashes; re-import/re-capture
-- consults the list so deletion sticks. Hashes, never the identifiers
-- themselves — the list must not re-store what erasure removed.
-- (retention_policy + the legal_hold columns shipped in 0010.)
CREATE TABLE erasure_suppression (
  workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  kind         text NOT NULL CHECK (kind IN ('email')),
  value_hash   text NOT NULL,  -- sha256 hex of the lowercased identifier
  created_at   timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (workspace_id, kind, value_hash)
);

ALTER TABLE erasure_suppression ENABLE ROW LEVEL SECURITY;
ALTER TABLE erasure_suppression FORCE ROW LEVEL SECURITY;
CREATE POLICY erasure_suppression_tenant_isolation ON erasure_suppression
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
