-- The scheduling contract (getAvailability/bookMeeting) names a meeting
-- host, but data-model.md §activity reserves assignee_id for tasks
-- (activity_task_fields) and gives the host no column — filed as
-- feedback/09. Additive column, meeting-only by CHECK, so free/busy can
-- be answered from the record.
ALTER TABLE activity
  ADD COLUMN host_user_id uuid NULL;

ALTER TABLE activity
  ADD CONSTRAINT activity_host_user_id_fkey FOREIGN KEY (workspace_id, host_user_id)
    REFERENCES app_user (workspace_id, id) ON DELETE SET NULL (host_user_id);

ALTER TABLE activity
  ADD CONSTRAINT activity_meeting_host CHECK (host_user_id IS NULL OR kind = 'meeting');

CREATE INDEX idx_activity_meeting_host
  ON activity (workspace_id, host_user_id, occurred_at)
  WHERE kind = 'meeting' AND archived_at IS NULL;
