-- ADR-0075/A121 §3a: serve the "which records did an AI write into?" review
-- filter.
--
-- A record's `captured_by` names who CREATED the row, and it is never
-- restamped — it is real provenance and the whole API reads it that way. But in
-- the connector path the AI does not create the record, it FILLS one: Gmail
-- capture mints the organization (`connector:gmail`), and then signature
-- enrichment renames it and the web dossier writes its profile fields and
-- facts, each of those rows stamped `agent:<task>`.
--
-- So "an AI wrote this" is a property of the CHILD rows, not of the record's
-- creator, and asking it means an EXISTS over them. These partial indexes are
-- what make that EXISTS cheap: they carry only the agent-written rows, which
-- are the minority, and they are keyed by the parent id the join uses.
--
-- The LIKE predicate is immutable, so it is index-legal. It is also the same
-- prefix grammar `captured_by` has everywhere else (`human:` | `agent:` |
-- `connector:` | `system:`).

CREATE INDEX idx_organization_profile_field_agent_written
  ON organization_profile_field (workspace_id, organization_id)
  WHERE captured_by LIKE 'agent:%';

CREATE INDEX idx_organization_fact_agent_written
  ON organization_fact (workspace_id, organization_id)
  WHERE captured_by LIKE 'agent:%';

CREATE INDEX idx_person_profile_field_agent_written
  ON person_profile_field (workspace_id, person_id)
  WHERE captured_by LIKE 'agent:%';
