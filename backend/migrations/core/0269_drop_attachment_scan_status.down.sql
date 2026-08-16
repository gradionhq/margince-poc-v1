-- Restore the column, with every existing row 'clean' rather than 'scanning'.
--
-- The default deliberately differs from 0070's. A row that was uploaded while
-- the column did not exist carries no verdict, and defaulting it to 'scanning'
-- would leave it permanently undownloadable behind a scanner this installation
-- does not run — the defect this pair of migrations removed. 0070 grandfathered
-- its own pre-existing rows to 'clean' for the same reason.
ALTER TABLE attachment ADD COLUMN scan_status text NOT NULL DEFAULT 'clean'
  CHECK (scan_status IN ('scanning', 'clean', 'blocked'));

-- 0070's exact index shape, workspace_id first. A down migration that restores
-- a different index than it removed leaves the reverse-reapply chain describing
-- a database no forward path ever built, and a tenant-scoped read needs the
-- leading workspace_id column to use it at all.
CREATE INDEX idx_attachment_scan_status ON attachment (workspace_id, scan_status)
  WHERE archived_at IS NULL;
