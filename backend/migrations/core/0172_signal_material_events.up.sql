-- 0172: the signal table gains the material events, and a key that makes a
-- dismissal stick (SIG-DDL-1, SIG-F-3, ADR-0079 arc).
--
-- The table has existed since 0047 with a render-ready card above it, and
-- nothing has ever written a row: the only writer is POST /signals, which is
-- human-only. Every account reads "no signal", including one whose own
-- correspondence says the contract ended.

ALTER TABLE signal DROP CONSTRAINT signal_kind_check;
ALTER TABLE signal ADD CONSTRAINT signal_kind_check CHECK (kind IN (
  'stalled_deal','champion_left','reengagement','buying_intent','risk','other',
  -- Read off the CONTENT of a thread (the signal_extract AI task).
  'contract_ended','new_opportunity','commitment_made',
  -- A comparison, not a judgment: an outbound tail nobody answered (SIG-F-3).
  'ghosted_thread'));

-- The dedupe key: kind ∥ subject ∥ the evidence it fired on. A producer that
-- runs hourly must raise nothing new on an unchanged account.
ALTER TABLE signal ADD COLUMN fingerprint text NULL;

-- Unique among open, acknowledged AND dismissed rows. An index that freed the
-- key on dismissal would let the next pass raise the same signal again, which
-- is the opposite of dismissing it. Only 'resolved' frees it, because a
-- resolved situation recurring IS a new fact about the account.
CREATE UNIQUE INDEX uq_signal_fingerprint ON signal (workspace_id, fingerprint)
  WHERE fingerprint IS NOT NULL AND status <> 'resolved' AND archived_at IS NULL;

-- Where the producer got to, per conversation. Cursor state, not a record
-- fact: no audit row and no outbox event, the same ruling user_record_view
-- carries (org360/doc.go). It is registered in tableownership_test.go on that
-- basis rather than exempted quietly.
CREATE TABLE signal_thread_scan (
  workspace_id     uuid NOT NULL REFERENCES workspace(id) ON DELETE CASCADE,
  -- The provider's own conversation id, as capture stamped it on the activity.
  thread_key       text NOT NULL,
  -- The newest message this scan has seen. A thread only re-enters the queue
  -- when something arrived after it, so a quiet conversation costs nothing.
  last_activity_at timestamptz NOT NULL,
  scanned_at       timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (workspace_id, thread_key)
);

ALTER TABLE signal_thread_scan ENABLE ROW LEVEL SECURITY;
ALTER TABLE signal_thread_scan FORCE ROW LEVEL SECURITY;
CREATE POLICY signal_thread_scan_tenant_isolation ON signal_thread_scan
  USING (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid)
  WITH CHECK (workspace_id = NULLIF(current_setting('app.workspace_id', true), '')::uuid);

GRANT SELECT, INSERT, UPDATE, DELETE ON signal_thread_scan TO margince_app;
