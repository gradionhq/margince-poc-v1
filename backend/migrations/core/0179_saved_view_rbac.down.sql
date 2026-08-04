-- Mirror of the up: it targeted the five system roles, so the down removes the
-- key from those five.
--
-- Honest limit: the up wrote only where the object was ABSENT, but this down
-- cannot tell a grant the up created from one that was already there — the
-- guard leaves no trace. Rolling back therefore removes a pre-existing
-- saved_view grant too. Re-applying the up restores the seeded default, which
-- is the same value for every system role here, so the round trip is lossless
-- in practice; it would not be for an object whose stored grant had been
-- hand-edited away from the default.

UPDATE role SET permissions = permissions #- '{objects,saved_view}'
  WHERE is_system AND key IN ('admin','manager','ops','rep','read_only');
