-- 0186: the correction ledger — a human's verdict on an AI-surfaced claim
-- (AIRT-SCHEMA-1 / AIRT-AC-9). DDL per the ai-runtime chapter.
--
-- Everything the product infers is re-derived, not stored: a brief line, a
-- moment, an inferred field. Re-derivation is what keeps those honest, and it
-- is also what makes them forget. Correct one and the next read asserts the
-- same wrong thing again, because nothing anywhere remembers that a human
-- already answered.
--
-- The key is why this works. claim_key is a STABLE hash of the claim's
-- normalized PATH — what the claim is about — and deliberately not of its
-- value. A verdict keyed on the value would evaporate the moment the evidence
-- shifted, which is exactly when the human's answer matters most; keyed on the
-- path, it survives every re-derivation of the same logical claim.
--
-- The table stores the human's verdict and never the model's asserted value.
-- There is nothing to be gained by keeping what was rejected, and a rejected
-- assertion sitting in a table is one bad join away from being shown again.
--
-- One row per (subject, claim) by the unique constraint below: a verdict is
-- the current answer, not an append-only opinion log. Re-deciding replaces,
-- and audit_log carries the history, as it does for every other mutation.

CREATE TABLE ai_feedback (
  id              uuid PRIMARY KEY DEFAULT uuidv7(),
  workspace_id    uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,

  -- The record the claim is about. Polymorphic because the ledger serves every
  -- surface that re-derives — a brief line on an organization, a moment on a
  -- person, an inferred field on a deal — and one ledger is what makes a
  -- correction on one screen bind on the others.
  subject_type    text NOT NULL CHECK (subject_type IN ('organization', 'person', 'deal', 'lead')),
  subject_id      uuid NOT NULL,

  claim_kind      text NOT NULL CHECK (claim_kind IN ('profile_field', 'inferred_kpi', 'next_step', 'signal', 'research_claim')),
  -- Stable within (subject, claim_kind): the hash of the claim's path, never
  -- its value.
  claim_key       text NOT NULL,

  -- `suppressed` is never shown again. `corrected` shows the human's value and
  -- is never overwritten by a fresh inference without a recorded 🟡 approval.
  -- `confirmed` may carry a "confirmed by" marker.
  verdict         text NOT NULL CHECK (verdict IN ('corrected', 'suppressed', 'confirmed')),
  corrected_value text NULL,
  note            text NULL,

  source          text NOT NULL,
  captured_by     text NOT NULL,
  version         bigint NOT NULL DEFAULT 1,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),

  -- A corrected verdict without a value is a claim that a human supplied one
  -- and lost it. The other two verdicts carry none by definition.
  CONSTRAINT ai_feedback_corrected_carries_a_value CHECK (
    (verdict = 'corrected') = (corrected_value IS NOT NULL)
  ),
  -- ONE current verdict per (subject, claim) — this is the suppression key the
  -- consult path looks up, so it has to be unique or "has a human answered
  -- this?" has more than one answer.
  UNIQUE (workspace_id, subject_type, subject_id, claim_kind, claim_key)
);

-- The consult path reads every verdict for one record at once, because a page
-- re-derives many claims about the same subject and asking per claim would be
-- a query per line.
CREATE INDEX idx_ai_feedback_subject ON ai_feedback (workspace_id, subject_type, subject_id);

ALTER TABLE ai_feedback ENABLE ROW LEVEL SECURITY;
ALTER TABLE ai_feedback FORCE ROW LEVEL SECURITY;
CREATE POLICY ai_feedback_tenant_isolation ON ai_feedback
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
