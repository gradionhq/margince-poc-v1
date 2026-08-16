-- 0268 down: remove the management role, restore the two default names.
--
-- Refuses while any user still holds `management`. Silently deleting the
-- role would cascade the assignments away and leave those people with no
-- role at all — a lockout nobody asked for. Reassign them first (the users
-- admin screen, or UPDATE role_assignment), then roll back.
DO $$
DECLARE holders integer;
BEGIN
  -- Lock the role row first: an assignment INSERT takes FOR KEY SHARE on
  -- its role, so holding FOR UPDATE here serializes the count against a
  -- concurrent assignment and the DELETE below cannot cascade one away.
  PERFORM 1 FROM role WHERE is_system AND key = 'management' FOR UPDATE;
  SELECT count(*) INTO holders
    FROM role_assignment ra JOIN role r ON r.id = ra.role_id
   WHERE r.is_system AND r.key = 'management';
  IF holders > 0 THEN
    RAISE EXCEPTION '0268 down: % user(s) still hold the management role; reassign them before rolling back', holders;
  END IF;
  DELETE FROM role WHERE is_system AND key = 'management';
END $$;

-- Mirror of the up guard: only a name still carrying the new default goes back.
UPDATE role SET name = 'Manager' WHERE is_system AND key = 'manager' AND name = 'Team Lead';
UPDATE role SET name = 'Rep'     WHERE is_system AND key = 'rep'     AND name = 'Member';
