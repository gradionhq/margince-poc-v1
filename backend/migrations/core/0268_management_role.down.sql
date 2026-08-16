-- 0268 down: remove the management role, restore the two default names.
--
-- Refuses while any user still holds `management`. Silently deleting the
-- role would cascade the assignments away and leave those people with no
-- role at all — a lockout nobody asked for. Reassign them first (the users
-- admin screen, or UPDATE role_assignment), then roll back.
DO $$
DECLARE holders integer;
BEGIN
  SELECT count(*) INTO holders
    FROM role_assignment ra JOIN role r ON r.id = ra.role_id
   WHERE r.key = 'management';
  IF holders > 0 THEN
    RAISE EXCEPTION '0268 down: % user(s) still hold the management role; reassign them before rolling back', holders;
  END IF;
END $$;

DELETE FROM role WHERE is_system AND key = 'management';

-- Mirror of the up guard: only a name still carrying the new default goes back.
UPDATE role SET name = 'Manager' WHERE is_system AND key = 'manager' AND name = 'Team Lead';
UPDATE role SET name = 'Rep'     WHERE is_system AND key = 'rep'     AND name = 'Member';
