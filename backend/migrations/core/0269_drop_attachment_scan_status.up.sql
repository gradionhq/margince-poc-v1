-- 0269: drop attachment.scan_status — the gate never had a verdict to gate on.
--
-- 0070 added the column and the download gate for a virus scanner that was
-- never integrated. The only path off the 'scanning' default was an injected
-- Scanner seam driven by tests and administration, so in a running
-- installation every uploaded file stayed 'scanning' forever and could never
-- be downloaded. 0070 already had to grandfather its existing rows to 'clean'
-- to avoid bricking them; every row created since has been stuck.
--
-- The gate promised a scan that does not happen, and a refusal a human cannot
-- resolve is worse than no refusal: it reads as a queue that will clear.
-- Downloads are gated by the attachment's parent record — object RBAC plus row
-- visibility, an invisible parent reading 404 — which is the authorization
-- that was actually doing the work.
--
-- Re-introducing malware scanning means re-introducing this column together
-- with a scanner that writes to it, not restoring the column alone.
DROP INDEX IF EXISTS idx_attachment_scan_status;

ALTER TABLE attachment DROP COLUMN IF EXISTS scan_status;
