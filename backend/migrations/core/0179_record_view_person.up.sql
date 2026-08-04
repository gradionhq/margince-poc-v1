-- 0171: the visit baseline covers people, not only organizations.
--
-- user_record_view was built generic — (entity_type, entity_id) — and then
-- pinned to a single value by its CHECK, because the company page was the
-- only record page that reported "what changed since you last looked".
-- The person page now reports it too, so the CHECK admits the second type.
--
-- Additive: no existing row changes, and the unique key already carries
-- entity_type, so a person and an organization sharing a uuid cannot
-- collide.
ALTER TABLE user_record_view
  DROP CONSTRAINT user_record_view_entity_type_check;

ALTER TABLE user_record_view
  ADD CONSTRAINT user_record_view_entity_type_check
  CHECK (entity_type IN ('organization', 'person'));
