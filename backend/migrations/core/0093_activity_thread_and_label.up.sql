-- ACT-DDL-1's pinned-but-never-built thread_key, plus the capture-classify
-- label columns (ADR-0063 §2.8). thread_key is the provider conversation
-- identity (Gmail threadId / Graph conversationId / RFC822 References root);
-- the reply-detection formula (CAP-FORMULA-1) joins on it. capture_label is
-- the batched AI classification; NULL means "not yet classified" and the
-- partial index IS the classify backlog — no work table.

ALTER TABLE activity ADD COLUMN thread_key text NULL;
ALTER TABLE activity ADD COLUMN capture_label text NULL
  CHECK (capture_label IS NULL OR capture_label IN ('commitment','meeting','noise'));
ALTER TABLE activity ADD COLUMN capture_labeled_at timestamptz NULL;

CREATE INDEX idx_activity_thread ON activity (workspace_id, thread_key)
  WHERE thread_key IS NOT NULL;

-- The classify backlog: connector-captured mail not yet labeled.
CREATE INDEX idx_activity_unlabeled ON activity (workspace_id, occurred_at)
  WHERE capture_label IS NULL AND captured_by LIKE 'connector:%' AND kind = 'email';
