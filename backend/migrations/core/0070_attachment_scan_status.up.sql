-- 0070: attachment.scan_status gates the download byte stream (RD-T05) —
-- 'scanning' and 'blocked' rows refuse the stream with a typed 409; the
-- metadata row itself stays disclosed. Lands as an on-row column so the
-- RLS/archive story stays on one row (no joined side-table). New rows
-- start 'scanning': absent an explicit verdict a row NEVER auto-transitions
-- to 'clean' — the injected Scanner seam in modules/activities is the only
-- path off it.
ALTER TABLE attachment ADD COLUMN scan_status text NOT NULL DEFAULT 'scanning'
  CHECK (scan_status IN ('scanning', 'clean', 'blocked'));

-- Rows that predate this migration were uploaded under the no-scanner
-- regime and have been downloadable all along; leaving them at the
-- 'scanning' default would brick every existing download behind a verdict
-- no scanner will ever deliver. They are grandfathered 'clean'; only rows
-- created from here on start 'scanning'.
UPDATE attachment SET scan_status = 'clean';

CREATE INDEX idx_attachment_scan_status ON attachment (workspace_id, scan_status)
  WHERE archived_at IS NULL;
