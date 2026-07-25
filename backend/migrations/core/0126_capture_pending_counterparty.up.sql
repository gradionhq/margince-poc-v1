-- ADR-0072/A118 (CAP-DDL-8): the capture disposition ledger.
--
-- ADR-0063 ensured a counterparty for every captured mail, which against a real
-- mailbox manufactures junk records from first-time strangers. The tiered gate
-- now defers that class (T4, ambiguous) instead of creating on sight: capture
-- writes a pending row and the verdict job resolves it. This table IS that
-- deferral — the work list, the claim lease, and the durable record of what was
-- decided about an address and why.
--
-- Written IN the capture transaction, so there is no crash window between an
-- activity landing and its disposition being known. The T2 suppressions record
-- here too (status 'suppressed'), which makes a wrong registry entry queryable
-- rather than only a system_log breadcrumb.
CREATE TABLE capture_pending_counterparty (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id uuid NOT NULL REFERENCES workspace(id) ON DELETE RESTRICT,

  -- The normalized identity this disposition is about. Lowercased at the write,
  -- matching activity.counterparty_email and person_email, so the verdict and
  -- the correspondence gate agree on what "the same address" means.
  email  text NOT NULL,
  domain text NULL,
  -- The header display name as captured: untrusted text, carried only so the
  -- review queue can show a human what arrived. Never a matching key.
  display_name text NULL,

  -- The activity that triggered the disposition — what a reviewer reads to
  -- decide, and what the accept path links the created records to.
  activity_id uuid NOT NULL,
  CONSTRAINT capture_pending_counterparty_activity_id_fkey
    FOREIGN KEY (workspace_id, activity_id)
    REFERENCES activity (workspace_id, id) ON DELETE CASCADE,

  -- The granting human the created records would be owned by, resolved from the
  -- connector principal at capture. Kept so a verdict arriving days later still
  -- creates under the human who authorized the capture, not the job.
  -- CASCADE, not RESTRICT: an OUTSIDER causes this row by mailing the connected
  -- mailbox, so residue a stranger planted must never block offboarding the
  -- human who happened to own the connection.
  owner_id uuid NOT NULL,
  CONSTRAINT capture_pending_counterparty_owner_id_fkey
    FOREIGN KEY (workspace_id, owner_id)
    REFERENCES app_user (workspace_id, id) ON DELETE CASCADE,

  -- Whether a created counterparty must stay person-only. The free-mail rule
  -- (CAP-PARAM-5) is decided by the TIER LADDER at capture time, so a verdict or
  -- a human accept arriving days later has to be told — recomputing it there
  -- would mean two places deciding what a domain can honestly name, and the
  -- reviewer's answer would be the one without the ladder's context.
  suppress_org boolean NOT NULL DEFAULT false,

  status text NOT NULL DEFAULT 'pending'
    CHECK (status IN ('pending', 'unsure', 'real', 'noise', 'suppressed', 'rejected')),
  -- Why this row reached its status: a T2 registry rule, a verdict, a human
  -- decision. Free text from a closed set the writer owns; ops reads it.
  disposition_reason text NULL,

  -- The claim lease for multi-replica FOR UPDATE SKIP LOCKED claiming. A row
  -- whose lease is in the past is claimable again, so a worker that died
  -- mid-batch releases its work by expiry rather than leaving it stuck.
  claimed_until timestamptz NULL,
  -- Which claim the current lease belongs to: a fresh token per ClaimDue, and
  -- the write key every resolution must present. Expiry alone cannot make a
  -- lease safe — a worker that stalled past its lease is still running, and the
  -- row it is holding may already have been re-claimed and re-judged by someone
  -- else. Without this the zombie's UPDATE still matches (the status is
  -- 'pending' again) and the stale verdict silently overwrites the live one.
  claimed_by uuid NULL,
  attempts      int NOT NULL DEFAULT 0,
  -- When the verdict job may next consider this row. NULL retires it from the
  -- due-scan: either resolved, or out of attempts. Bounded retries live in the
  -- engine; this column is what the partial index below scans.
  -- No DEFAULT on purpose: every writer supplies this explicitly, and a default
  -- of now() would make an insert that omits the column (a suppressed or an
  -- unsure row) instantly due for a verdict it should never receive.
  next_attempt_at timestamptz NULL,

  -- The 🟡 review-queue proposal an 'unsure' verdict staged, so a re-run finds
  -- the existing offer instead of staging a duplicate.
  proposal_id uuid NULL,
  CONSTRAINT capture_pending_counterparty_proposal_id_fkey
    FOREIGN KEY (workspace_id, proposal_id)
    REFERENCES approval (workspace_id, id) ON DELETE SET NULL,

  resolved_at timestamptz NULL,
  -- When the noise disposition's content redaction actually ran. The undo
  -- window is measured from resolved_at, and this column is what makes the
  -- sweep resumable: a crash mid-sweep leaves the rows it had not reached
  -- still due, and re-running redacts them rather than starting over.
  redacted_at timestamptz NULL,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now()
);

-- One LIVE row per address: a second mail from the same stranger joins the
-- existing disposition instead of queuing a second verdict for the same
-- question.
CREATE UNIQUE INDEX idx_capture_pending_counterparty_live
  ON capture_pending_counterparty (workspace_id, email)
  WHERE status IN ('pending', 'unsure');

-- And one SUPPRESSED row per address, for the same reason in the other
-- direction. A suppressed status sits outside the live index, so without this
-- every message from a newsletter or an ESP would append another row and the
-- table would grow with total suppressed mail volume — turning "the reason is
-- queryable" into millions of duplicates of one answer.
CREATE UNIQUE INDEX idx_capture_pending_counterparty_suppressed
  ON capture_pending_counterparty (workspace_id, email)
  WHERE status = 'suppressed';

-- The verdict job's due-scan. Partial on next_attempt_at so resolved and
-- exhausted rows cost the scan nothing.
CREATE INDEX idx_capture_pending_counterparty_due
  ON capture_pending_counterparty (next_attempt_at)
  WHERE next_attempt_at IS NOT NULL;

-- The redaction sweep's due-scan: noise dispositions whose undo window has run
-- out and whose content is still there. Partial, so the index holds only rows
-- with redaction outstanding — it empties as the sweep catches up rather than
-- growing with every noise verdict ever made.
CREATE INDEX idx_capture_pending_counterparty_redaction_due
  ON capture_pending_counterparty (resolved_at)
  WHERE status = 'noise' AND redacted_at IS NULL;

ALTER TABLE capture_pending_counterparty ENABLE ROW LEVEL SECURITY;
ALTER TABLE capture_pending_counterparty FORCE ROW LEVEL SECURITY;
CREATE POLICY capture_pending_counterparty_tenant_isolation ON capture_pending_counterparty
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);
