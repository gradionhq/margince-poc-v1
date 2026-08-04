-- Narrowing back to organizations only would orphan any person baseline the
-- forward migration allowed, so drop those rows first: they are view state
-- (one user's reading history), never a record fact, and the page treats a
-- missing baseline as an honest first visit.
DELETE FROM user_record_view WHERE entity_type = 'person';

ALTER TABLE user_record_view
  DROP CONSTRAINT user_record_view_entity_type_check;

ALTER TABLE user_record_view
  ADD CONSTRAINT user_record_view_entity_type_check
  CHECK (entity_type IN ('organization'));
