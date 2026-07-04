DROP INDEX idx_event_outbox_unpublished;
CREATE INDEX idx_event_outbox_unpublished ON event_outbox (created_at) WHERE published_at IS NULL;
ALTER TABLE event_outbox DROP COLUMN seq;
ALTER TABLE audit_log DROP CONSTRAINT audit_log_actor_type_check;
ALTER TABLE audit_log ADD CONSTRAINT audit_log_actor_type_check
  CHECK (actor_type IN ('human','agent','system'));
