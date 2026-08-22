-- A signal may be about a PROJECT, and the producers may raise one when a
-- project in flight has gone quiet. The two CHECKs are the schema's word on
-- which subjects and kinds a signal row can carry; the signals store keeps the
-- same two sets in Go and a test holds them to these constraints.
SET LOCAL lock_timeout = '5s';
ALTER TABLE signal DROP CONSTRAINT signal_entity_type_check;
ALTER TABLE signal ADD CONSTRAINT signal_entity_type_check
    CHECK (entity_type IS NULL OR entity_type IN ('deal', 'organization', 'person', 'project'));
ALTER TABLE signal DROP CONSTRAINT signal_kind_check;
ALTER TABLE signal ADD CONSTRAINT signal_kind_check
    CHECK (kind IN ('stalled_deal', 'champion_left', 'reengagement', 'buying_intent', 'risk', 'other',
                    'contract_ended', 'new_opportunity', 'commitment_made', 'ghosted_thread',
                    'project_gone_quiet'));
