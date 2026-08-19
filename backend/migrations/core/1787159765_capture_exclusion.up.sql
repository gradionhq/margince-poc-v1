-- Pre-capture exclusions: addresses and domains whose mail the CRM must not
-- store at all, checked in the capture sink before any row is written.
--
-- Two scopes. A WORKSPACE exclusion is the installation's rule (an auditor's
-- address, a payroll provider's domain) and takes admin/ops to change. A USER
-- exclusion is one person's boundary for the mailbox they connected — their
-- spouse, their doctor — and binds only the connections that person granted.
-- The former per-user rule set (migration 0076, retired by 0165) was withdrawn
-- because it was the only boundary on a workspace-shared record set; it returns
-- beside a workspace list and a per-message audience, as one of three.
--
-- value is stored folded: a lowercased address, or an IDNA ASCII domain.
SET LOCAL lock_timeout = '3s';

CREATE TABLE capture_exclusion (
  id         uuid PRIMARY KEY DEFAULT uuidv7(),
  scope      text NOT NULL CHECK (scope IN ('workspace', 'user')),
  user_id    uuid NULL REFERENCES app_user (id) ON DELETE CASCADE,
  kind       text NOT NULL CHECK (kind IN ('address', 'domain')),
  value      text NOT NULL CHECK (value = lower(value) AND value <> ''),
  created_by text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  -- A user exclusion names its user; a workspace one names nobody.
  CONSTRAINT capture_exclusion_scope_user CHECK ((scope = 'user') = (user_id IS NOT NULL))
);

-- One row per (scope, user, kind, value): re-adding is idempotent.
CREATE UNIQUE INDEX uq_capture_exclusion
  ON capture_exclusion (scope, coalesce(user_id, '00000000-0000-0000-0000-000000000000'::uuid), kind, value);
-- The sink asks "is any of this message's addresses excluded for this mailbox
-- owner or the workspace" per captured message.
CREATE INDEX idx_capture_exclusion_value ON capture_exclusion (kind, value);

-- The trace's stage/outcome pairing gains the exclusion drop: the same
-- pre-store stage as the colleagues gate, with the `suppressed` outcome the
-- ladder already uses for "a rule kept this out", and a reason naming the
-- kind of rule.
ALTER TABLE capture_trace DROP CONSTRAINT capture_trace_stage_outcome_check;
ALTER TABLE capture_trace ADD CONSTRAINT capture_trace_stage_outcome_check CHECK (
     (stage = 'internal_drop'  AND outcome IN ('internal', 'suppressed'))
  OR (stage = 'activity_write' AND outcome = 'fault')
  OR (stage = 'tier_ladder'    AND outcome IN ('captured', 'suppressed', 'deferred', 'fault'))
);
