-- 0111: index the classify-labeled-message count (ADR-0068).
-- LabeledCaptureCountSince (activities/capturelabelstats.go) runs a synchronous
-- count(*) FROM activity WHERE capture_labeled_at >= $1 on every backfill cost
-- preview — the exact observed denominator for classify's per-message cost.
-- activity's existing indexes are all occurred_at-based (migration 0008), so
-- that count would sequential-scan the workspace's whole timeline. This partial
-- index covers the predicate (workspace-scoped, on the labeled instant) and
-- indexes only the labeled rows, which are a small fraction of activity.
CREATE INDEX idx_activity_labeled ON activity (workspace_id, capture_labeled_at)
  WHERE capture_labeled_at IS NOT NULL;
