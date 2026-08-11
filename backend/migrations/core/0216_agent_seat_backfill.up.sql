-- Give every installation that already booted the first-party Agent Runner
-- identity bootstrap now writes for a new one (seed-and-fixtures §1.5).
--
-- Bootstrap runs once, against an empty database, so it reaches no installation
-- that exists. Without this half the seat would arrive only for installations
-- created from here on, and every deployed one would stay permanently unable to
-- run an actor-less job with nothing to show that it was missing anything.
--
-- Additive repair rather than an edit to the bootstrap migration, for the reason
-- CLAUDE.md gives: an applied version never re-runs, so editing history changes
-- what FRESH installations get and leaves deployed databases exactly as they
-- were, with the two silently diverging.
--
-- app_user carries FORCE row-level security with deny-on-unset, and FORCE binds
-- the table owner — the role migrations run as. The per-iteration binding is
-- what lets the INSERT's WITH CHECK pass at all. The workspace predicate is the
-- other half and does a different job: an executor row-level security does not
-- filter — a superuser or a BYPASSRLS role, which is what dev machines and CI
-- run as — sees every workspace on every iteration, so without it the guard
-- would be answered by another tenant's seat and this would insert once per
-- workspace into the first one.
DO $$
DECLARE ws uuid;
BEGIN
  FOR ws IN SELECT id FROM workspace LOOP
    PERFORM set_config('app.workspace_id', ws::text, true);

    -- The guard asks for ANY agent row, archived and deactivated included. A
    -- workspace whose seat an operator archived or switched off is in a state
    -- they chose, and minting a second one would overrule it — while colliding
    -- with the archived row's address on the case-insensitive email index if it
    -- was seeded by this same statement earlier. Such a workspace stays visible
    -- in the seatless gauge, which is the honest reading of it.
    -- The zone comes from the installation setting because the workspace row no
    -- longer carries one (ADR-0090/A135 moved the installation's identity into
    -- setting rows). It is the same value bootstrap hands the seat it writes, so
    -- the two paths produce one row rather than two that differ by provenance.
    -- COALESCE for the installation that predates those settings: the column's
    -- own default is UTC, and a seat displays no times to anybody in any case.
    INSERT INTO app_user (workspace_id, email, display_name, timezone, is_agent, seat_type, status)
    SELECT w.id, 'agent@' || w.slug || '.gradion.local', 'Gradion Agent',
           COALESCE((SELECT s.value #>> '{}' FROM setting s WHERE s.key = 'installation.timezone'), 'UTC'),
           true, 'full', 'active'
      FROM workspace w
     WHERE w.id = ws
       AND NOT EXISTS (SELECT 1 FROM app_user u WHERE u.workspace_id = ws AND u.is_agent);
  END LOOP;
END $$;
