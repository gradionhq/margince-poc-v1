-- ADR-0075/A121 §3a: serve the "which records did an AI write into?" review
-- filter.
--
-- A record's `captured_by` names who CREATED the row and is never restamped —
-- it is real provenance and the whole API reads it that way. But in the
-- connector path the AI does not create the record, it FILLS one: Gmail capture
-- mints the organization (`connector:gmail`), and then the AI renames it from a
-- signature and writes its profile. So "an AI wrote this" cannot be read off
-- the record; it has to be asked of the record's history.
--
-- The audit log is that history, and it is complete BY CONSTRUCTION: every
-- mutation commits its domain row, its audit row and its outbox row in one
-- transaction, at one store chokepoint. No agent write reaches a record without
-- leaving a row here. Any narrower source is a list somebody has to maintain.
--
-- The predicate matches `actor_id LIKE 'agent:%'`, not `actor_type`. Those are
-- different axes: actor_type is the principal MECHANISM (a background job runs
-- as 'system') while actor_id carries the <kind>:<id> identity `captured_by`
-- uses everywhere else. The deep-read worker is a system principal whose
-- identity is `agent:deepread`, so typing on the mechanism would miss every AI
-- enrichment this filter exists to surface.
--
-- idx_audit_entity (0012) already covers (workspace_id, entity_type, entity_id).
-- This adds the selectivity that matters for the EXISTS: only agent-written
-- rows, which are a small minority of an append-only log that holds every
-- mutation the installation has ever made.
CREATE INDEX idx_audit_log_agent_actor
  ON audit_log (workspace_id, entity_type, entity_id)
  WHERE actor_id LIKE 'agent:%';
