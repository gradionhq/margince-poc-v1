-- 0047: signal + signal_resolution — the warm-room signal substrate
-- (B-E08.1, data-model §12.5, features/07 §9). Company-level and
-- consent-gated by construction (P12): the only mandatory attribution is
-- organizational (resolved_org_id after resolution); resolved_person_id
-- is nullable and set only under a recorded consent grant. A signal that
-- cannot be attributed to an organization is DROPPED, never retained as
-- a person-level dossier.
--
-- One §12.5 deviation, deliberate: entity_type/entity_id are NULLABLE
-- (set together) instead of NOT NULL. A raw inbound/web item carries only
-- its raw_ref until the resolver attributes it — a mandatory subject at
-- ingest would force exactly the speculative person/org link the epic's
-- drop guard forbids. A resolved signal always has a subject (CHECK).
CREATE TABLE signal (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id  uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  kind          text NOT NULL CHECK (kind IN ('stalled_deal','champion_left','reengagement','buying_intent','risk','other')),
  source_channel text NOT NULL DEFAULT 'derived' CHECK (source_channel IN ('derived','inbound','web','social','deal_room_engagement')),
  raw_ref       text NULL,                               -- pointer to the raw source payload (handle / domain / mention / url)
  entity_type   text NULL CHECK (entity_type IS NULL OR entity_type IN ('deal','organization','person')),
  entity_id     uuid NULL,                               -- polymorphic subject ref (no FK, like audit_log.entity_id)
  resolution_state text NOT NULL DEFAULT 'resolved' CHECK (resolution_state IN ('resolved','low_confidence','unresolved','dropped')),
  resolution_confidence numeric NULL CHECK (resolution_confidence IS NULL OR (resolution_confidence >= 0 AND resolution_confidence <= 1)),
  resolved_org_id    uuid NULL,
  resolved_person_id uuid NULL,                          -- optional; only under recorded consent (P12)
  severity      text NOT NULL DEFAULT 'info' CHECK (severity IN ('info','warn','urgent')),
  summary       text NOT NULL,
  evidence      jsonb NOT NULL DEFAULT '[]',             -- per-claim {snippet, source_type, source_id}
  status        text NOT NULL DEFAULT 'open' CHECK (status IN ('open','acknowledged','resolved','dismissed')),
  detected_at   timestamptz NOT NULL DEFAULT now(),
  source        text NOT NULL,
  captured_by   text NOT NULL,
  version       bigint NOT NULL DEFAULT 1,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  archived_at   timestamptz NULL,

  CONSTRAINT signal_entity_pair CHECK ((entity_type IS NULL) = (entity_id IS NULL)),
  -- a confidently resolved signal always has a subject record
  CONSTRAINT signal_resolved_has_entity CHECK (resolution_state <> 'resolved' OR entity_type IS NOT NULL),
  -- target of the composite tenant FK from signal_resolution (0019 pattern)
  CONSTRAINT uq_signal_ws_id UNIQUE (workspace_id, id),
  -- tenant-pinned resolution references: never straddle workspaces
  CONSTRAINT signal_resolved_org_fkey FOREIGN KEY (workspace_id, resolved_org_id)
    REFERENCES organization (workspace_id, id) ON DELETE SET NULL (resolved_org_id),
  CONSTRAINT signal_resolved_person_fkey FOREIGN KEY (workspace_id, resolved_person_id)
    REFERENCES person (workspace_id, id) ON DELETE SET NULL (resolved_person_id)
);
CREATE INDEX idx_signal_open ON signal (workspace_id, status, severity, detected_at DESC);
CREATE INDEX idx_signal_unresolved ON signal (workspace_id, resolution_state, detected_at DESC);
CREATE TRIGGER trg_signal_updated BEFORE UPDATE ON signal
  FOR EACH ROW EXECUTE FUNCTION set_updated_at_bump_version();

ALTER TABLE signal ENABLE ROW LEVEL SECURITY;
ALTER TABLE signal FORCE ROW LEVEL SECURITY;
CREATE POLICY signal_tenant_isolation ON signal
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

-- Append-only log carrying BOTH the resolver's inspectable match basis
-- (matched_on / matched_org_id / match_confidence / match_detail) and the
-- later human outcome (outcome / note / resolved_by) — rows are
-- distinguished by which column group is non-null. Never updated.
CREATE TABLE signal_resolution (
  id            uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id  uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,
  signal_id     uuid NOT NULL,
  matched_on    text NULL CHECK (matched_on IS NULL OR matched_on IN ('domain','name','prior_interaction','manual','none')),
  matched_org_id uuid NULL,
  match_confidence numeric NULL CHECK (match_confidence IS NULL OR (match_confidence >= 0 AND match_confidence <= 1)),
  match_detail  jsonb NULL,                              -- {candidates:[…], chosen, reason}
  outcome       text NULL CHECK (outcome IS NULL OR outcome IN ('acknowledged','resolved','dismissed')),
  note          text NULL,
  resolved_by   uuid NULL,
  source        text NOT NULL,
  captured_by   text NOT NULL,
  created_at    timestamptz NOT NULL DEFAULT now(),

  CONSTRAINT sigres_signal_fkey FOREIGN KEY (workspace_id, signal_id)
    REFERENCES signal (workspace_id, id) ON DELETE CASCADE,
  CONSTRAINT sigres_org_fkey FOREIGN KEY (workspace_id, matched_org_id)
    REFERENCES organization (workspace_id, id) ON DELETE SET NULL (matched_org_id),
  CONSTRAINT sigres_resolved_by_fkey FOREIGN KEY (workspace_id, resolved_by)
    REFERENCES app_user (workspace_id, id) ON DELETE SET NULL (resolved_by)
);
CREATE INDEX idx_sigres_signal ON signal_resolution (workspace_id, signal_id, created_at DESC);

ALTER TABLE signal_resolution ENABLE ROW LEVEL SECURITY;
ALTER TABLE signal_resolution FORCE ROW LEVEL SECURITY;
CREATE POLICY signal_resolution_tenant_isolation ON signal_resolution
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

-- The resolver's stamp is a first-class audited verb — same additive
-- vocabulary move as 0018/0024/0026/0028.
ALTER TABLE audit_log DROP CONSTRAINT audit_log_action_check;
ALTER TABLE audit_log ADD CONSTRAINT audit_log_action_check
  CHECK (action IN ('create','update','archive','merge','promote','restore','export','erase',
                    'login','assign','advance_stage','approve','reject',
                    'consent_grant','consent_withdraw','activity_relink',
                    'record_share','record_unshare','resolve'));

-- Backfill the `signal` RBAC object into the seeded system-role policy
-- documents of EXISTING workspaces (new workspaces get it from the
-- code-side seed). Posture mirrors the record types: reps create and
-- triage signals but never delete them; managers/admin/ops carry delete.
UPDATE role SET permissions = jsonb_set(
  permissions, '{objects,signal}',
  '{"create":true,"read":true,"update":true,"delete":true}'::jsonb)
WHERE is_system AND key IN ('admin','ops','manager')
  AND NOT permissions->'objects' ? 'signal';
UPDATE role SET permissions = jsonb_set(
  permissions, '{objects,signal}',
  '{"create":true,"read":true,"update":true,"delete":false}'::jsonb)
WHERE is_system AND key = 'rep'
  AND NOT permissions->'objects' ? 'signal';
UPDATE role SET permissions = jsonb_set(
  permissions, '{objects,signal}',
  '{"create":false,"read":true,"update":false,"delete":false}'::jsonb)
WHERE is_system AND key = 'read_only'
  AND NOT permissions->'objects' ? 'signal';
