-- Transactional outbox (events.md §4.2): the domain write, its audit_log
-- row, and this outbox row commit in ONE transaction; the relay job then
-- XADDs unpublished rows to Redis Streams in created_at order and stamps
-- published_at. At-least-once; consumers dedupe on envelope event_id.
-- The DDL lives in events.md prose, not data-model.md (fable feedback 05);
-- it is infra-owned and carries no RLS — tenancy rides inside the envelope.

CREATE TABLE event_outbox (
  id           uuid PRIMARY KEY DEFAULT uuidv7(),
  stream       text NOT NULL,               -- e.g. 'gw:events:crm:deal'
  envelope     jsonb NOT NULL,              -- the full typed envelope (events.md §2)
  published_at timestamptz NULL,
  created_at   timestamptz NOT NULL DEFAULT now()
);
-- The relay poll: unpublished rows in commit order.
CREATE INDEX idx_event_outbox_unpublished ON event_outbox (created_at) WHERE published_at IS NULL;
