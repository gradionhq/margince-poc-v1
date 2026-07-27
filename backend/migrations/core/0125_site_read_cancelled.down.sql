UPDATE site_read SET status = 'failed' WHERE status = 'cancelled';
ALTER TABLE site_read DROP CONSTRAINT site_read_status_check;
ALTER TABLE site_read ADD CONSTRAINT site_read_status_check
  CHECK (status IN ('queued', 'deferred', 'running', 'done', 'partial', 'failed'));
