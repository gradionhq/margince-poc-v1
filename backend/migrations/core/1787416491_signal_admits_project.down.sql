-- Rows carrying the project subject or the project_gone_quiet kind would
-- violate the narrower constraints, so they are removed before the CHECKs
-- are put back.
SET LOCAL lock_timeout = '5s';
DELETE FROM signal WHERE entity_type = 'project' OR kind = 'project_gone_quiet';
ALTER TABLE signal DROP CONSTRAINT signal_entity_type_check;
ALTER TABLE signal ADD CONSTRAINT signal_entity_type_check
    CHECK (entity_type IS NULL OR entity_type IN ('deal', 'organization', 'person'));
ALTER TABLE signal DROP CONSTRAINT signal_kind_check;
ALTER TABLE signal ADD CONSTRAINT signal_kind_check
    CHECK (kind IN ('stalled_deal', 'champion_left', 'reengagement', 'buying_intent', 'risk', 'other',
                    'contract_ended', 'new_opportunity', 'commitment_made', 'ghosted_thread'));
