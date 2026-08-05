-- Narrowing back to organizations only would orphan any person baseline the
-- forward migration allowed, so drop those rows first: they are view state
-- (one user's reading history), never a record fact, and the page treats a
-- missing baseline as an honest first visit.
--
-- The RLS bypass is what makes the rollback actually work. user_record_view
-- carries FORCE ROW LEVEL SECURITY, and the migration connection sets no
-- app.workspace_id — so a plain DELETE matches ZERO rows in every workspace,
-- and the narrowed CHECK below then rejects the person rows still sitting
-- there. The rollback would fail on any installation where somebody had
-- opened a person page. Disabled and restored in the same statement pair so
-- the table never ends up unprotected.
ALTER TABLE user_record_view NO FORCE ROW LEVEL SECURITY;
DELETE FROM user_record_view WHERE entity_type = 'person';
ALTER TABLE user_record_view FORCE ROW LEVEL SECURITY;

ALTER TABLE user_record_view
  DROP CONSTRAINT user_record_view_entity_type_check;

ALTER TABLE user_record_view
  ADD CONSTRAINT user_record_view_entity_type_check
  CHECK (entity_type IN ('organization'));
