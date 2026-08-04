DROP INDEX uq_signal_fingerprint;
CREATE UNIQUE INDEX uq_signal_fingerprint ON signal (workspace_id, fingerprint)
  WHERE fingerprint IS NOT NULL AND status <> 'resolved' AND archived_at IS NULL;

ALTER TABLE signal_thread_scan DROP COLUMN message_count;
