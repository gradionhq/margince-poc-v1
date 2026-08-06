-- Narrowing back to organizations only would orphan any person baseline the
-- forward migration allowed, so drop those rows first: they are view state
-- (one user's reading history), never a record fact, and the page treats a
-- missing baseline as an honest first visit.
--
-- The workspace loop is what makes the rollback actually work.
-- user_record_view carries FORCE ROW LEVEL SECURITY, and an unbound DELETE
-- matches ZERO rows in every workspace — the narrowed CHECK below would then
-- reject the person rows still sitting there, failing the rollback on any
-- installation where somebody had opened a person page.
DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);
    DELETE FROM user_record_view
    WHERE (entity_type = 'person')
      AND user_record_view.workspace_id = ws;
  END LOOP;
END $$;

ALTER TABLE user_record_view
  DROP CONSTRAINT user_record_view_entity_type_check;

ALTER TABLE user_record_view
  ADD CONSTRAINT user_record_view_entity_type_check
  CHECK (entity_type IN ('organization'));
