-- Mirror of the up: it targeted the five system roles, so the down removes the
-- key from those five.
--
-- Honest limit: the up wrote only where the object was ABSENT, but this down
-- cannot tell a grant the up created from one that was already there — the
-- guard leaves no trace. Rolling back therefore removes a pre-existing
-- relationship grant too.

UPDATE role SET permissions = permissions #- '{objects,relationship}'
  WHERE is_system AND key IN ('admin','manager','ops','rep','read_only');
