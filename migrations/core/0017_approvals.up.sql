-- The 🟡 confirm-first approval inbox (ADR-0036, features/07 §8): a
-- staged agent action awaiting a human decision. The staged row IS the
-- authority object — approval is bound to the exact proposed change
-- (diff_hash), the staging passport, and the target row's version, and
-- is consumed exactly once.

CREATE TABLE approval (
  id                 uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id       uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,

  kind               text NOT NULL,             -- the staged tool/moment: advance_deal | promote_lead | archive_record | …
  status             text NOT NULL DEFAULT 'pending'
                     CHECK (status IN ('pending','approved','rejected','expired')),

  proposed_by        text NOT NULL,             -- agent:<passport-id> / connector:<n>
  on_behalf_of       uuid NULL REFERENCES app_user(id) ON DELETE SET NULL,
  passport_id        uuid NULL REFERENCES passport(id) ON DELETE SET NULL,

  target_entity_type text NULL,
  target_entity_id   uuid NULL,
  -- Row version when the diff was staged; redemption re-reads and
  -- rejects on mismatch (the world changed since the human saw the diff).
  target_version     bigint NULL,

  summary            text NULL,
  proposed_change    jsonb NOT NULL,
  diff_hash          text NOT NULL,             -- sha256 of the canonical proposed_change

  expires_at         timestamptz NOT NULL,      -- unactioned staging rots; expired is not approvable
  decided_by         uuid NULL REFERENCES app_user(id) ON DELETE SET NULL,
  decided_at         timestamptz NULL,
  decision_reason    text NULL,
  -- Single-use redemption: stamped when the approved effect executes.
  consumed_at        timestamptz NULL,

  version            bigint NOT NULL DEFAULT 1,
  created_at         timestamptz NOT NULL DEFAULT now(),
  updated_at         timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT approval_decided CHECK (
    (status = 'pending' AND decided_at IS NULL) OR
    (status = 'expired') OR
    (status IN ('approved','rejected') AND decided_at IS NOT NULL)
  )
);

CREATE INDEX idx_approval_inbox ON approval (workspace_id, created_at) WHERE status = 'pending';
CREATE INDEX idx_approval_target ON approval (target_entity_id) WHERE target_entity_id IS NOT NULL;

CREATE TRIGGER trg_approval_updated BEFORE UPDATE ON approval
  FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

-- Tenant table ⇒ RLS, same deny-on-unset policy as every other
-- (the coverage fitness test refuses a workspace_id table without it).
ALTER TABLE approval ENABLE ROW LEVEL SECURITY;
ALTER TABLE approval FORCE ROW LEVEL SECURITY;
CREATE POLICY approval_tenant_isolation ON approval
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
