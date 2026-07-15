-- seed-dev.sql — the dev-database seed for demo data that has no public API.
--
-- Companion to scripts/seed-dev.sh (the API seed for people/orgs/deals). This
-- file holds dev/demo data that can only be written directly to the database —
-- reference tables and config the product intentionally exposes no REST/MCP
-- endpoint for. It is part of the default dev-env init: `make dev` applies it on
-- boot and `make seed-dev` re-applies it, both AFTER the API seed has created
-- the demo workspace. So a developer runs `make dev && make seed-dev` and every
-- surface is testable with the necessary data pre-filled. Idempotent — safe to
-- re-run; extend it as more API-less demo data or settings are needed.
--
-- Seeds two things, demo-workspace only:
--   1. FX rates (fx_rate is an exchange-rate feed — no API, no audit_log/
--      event_outbox, and the product never invents a rate at runtime, so a
--      non-EUR deal cannot be won without a seeded rate). Never seeded at
--      workspace bootstrap, so real workspaces keep the honest "no rate → 422,
--      never rate=1" behaviour.
--   2. The RBAC demo fixture the sharing/roles surfaces need: two non-admin
--      seats (Rep One, team-scoped; Rep Two, own-scoped), the DACH Sales team,
--      their role assignments, and admin-ownership of the API-seeded records so
--      row scope actually restricts them. See the demo-accounts manifest below.
--
-- Requires the compose stack's Postgres (make seed-dev-db runs it as
-- margince_owner; make dev applies it over the dev owner DSN).
--
-- Demo accounts — the ONE place the dev login credentials are described. The
-- `make dev` ready banner (scripts/dev.sh) prints the lines between the markers
-- below verbatim, so there is no second copy to drift. Fixed demo values on a
-- throwaway localhost DB (admin is bootstrapped by scripts/seed-dev.sh; the two
-- reps by this file) — never real credentials. Keep this in sync with the
-- INSERTs below.
-- DEMO-ACCOUNTS-BEGIN
-- workspace demo-workspace  ·  password (all three): demo-password-123
-- admin@demo.test   admin       — sees every record
-- rep@demo.test     rep         — team-scoped (team DACH Sales)
-- rep2@demo.test    individual  — own-scoped, no team (sees only what's shared)
-- DEMO-ACCOUNTS-END

BEGIN;

DO $$
DECLARE
  ws uuid;
BEGIN
  SELECT id INTO ws FROM workspace WHERE slug = 'demo-workspace';
  IF ws IS NULL THEN
    RAISE NOTICE 'seed-dev.sql: no demo-workspace row — run make seed-dev first';
    RETURN;
  END IF;

  -- Tenant tables carry FORCE RLS; bind the GUC so writes are visible even on a
  -- non-superuser owner connection (mirrors seed-reset.sql).
  PERFORM set_config('app.workspace_id', ws::text, true);

  -- FX rates: base currency is EUR; seed the three other UI currencies
  -- (USD/GBP/CHF) dated today so a close on or after today finds a rate.
  -- Representative demo values — not a live quote.
  INSERT INTO fx_rate (workspace_id, from_currency, to_currency, rate, rate_date)
  VALUES
    (ws, 'USD', 'EUR', 0.92, CURRENT_DATE),
    (ws, 'GBP', 'EUR', 1.17, CURRENT_DATE),
    (ws, 'CHF', 'EUR', 1.04, CURRENT_DATE)
  ON CONFLICT (workspace_id, from_currency, to_currency, rate_date)
    DO UPDATE SET rate = EXCLUDED.rate;

  RAISE NOTICE 'seed-dev.sql: FX rates USD/GBP/CHF → EUR seeded for demo-workspace (rate_date=%)', CURRENT_DATE;
END $$;

DO $$
DECLARE
  ws uuid;
  admin_id uuid;
  admin_hash text;
  rep_id uuid;
  rep2_id uuid;
  dach_team_id uuid;
BEGIN
  SELECT id INTO ws FROM workspace WHERE slug = 'demo-workspace';
  IF ws IS NULL THEN
    RAISE NOTICE 'seed-dev.sql: no demo-workspace row — run make seed-dev first';
    RETURN;
  END IF;

  -- Tenant tables carry FORCE RLS; bind the GUC so writes are visible even on a
  -- non-superuser owner connection (mirrors seed-reset.sql).
  PERFORM set_config('app.workspace_id', ws::text, true);

  -- The demo admin is bootstrapped through the public API (scripts/seed-dev.sh),
  -- never here — reuse its password_hash verbatim so the 2nd seat shares the
  -- demo password, without re-implementing Argon2id hashing in SQL.
  SELECT id, password_hash INTO admin_id, admin_hash
    FROM app_user
    WHERE workspace_id = ws AND lower(email) = lower('admin@demo.test');
  IF admin_id IS NULL THEN
    RAISE NOTICE 'seed-dev.sql: no admin@demo.test user — run make seed-dev first';
    RETURN;
  END IF;

  -- The API seed (seed-dev.sh) creates people/orgs/deals with NO owner, and an
  -- ownerless row is workspace-shared — visible at EVERY row scope. That would
  -- let the own-scoped Rep Two (below) see everything and make record sharing
  -- unobservable. Make Demo Admin the owner of every ownerless seeded record so
  -- row scope actually bites (captured_by is already the admin). Idempotent —
  -- only touches rows that are still ownerless.
  UPDATE person       SET owner_id = admin_id WHERE workspace_id = ws AND owner_id IS NULL;
  UPDATE organization SET owner_id = admin_id WHERE workspace_id = ws AND owner_id IS NULL;
  UPDATE deal         SET owner_id = admin_id WHERE workspace_id = ws AND owner_id IS NULL;
  UPDATE lead         SET owner_id = admin_id WHERE workspace_id = ws AND owner_id IS NULL;

  -- 2nd full-seat user so the Share picker / "who has access" have a real
  -- subject beyond the lone admin.
  INSERT INTO app_user (workspace_id, email, password_hash, display_name, seat_type, status)
  VALUES (ws, 'rep@demo.test', admin_hash, 'Rep One', 'full', 'active')
  ON CONFLICT (workspace_id, lower(email)) DO NOTHING;

  SELECT id INTO rep_id
    FROM app_user
    WHERE workspace_id = ws AND lower(email) = lower('rep@demo.test');

  -- A team with admin + Rep One as members, so the roster picker and the
  -- "who has access" list have a demonstrable, non-trivial membership.
  INSERT INTO team (workspace_id, name)
  VALUES (ws, 'DACH Sales')
  ON CONFLICT (workspace_id, name) DO NOTHING;

  SELECT id INTO dach_team_id
    FROM team
    WHERE workspace_id = ws AND name = 'DACH Sales';

  INSERT INTO team_membership (workspace_id, team_id, user_id)
  VALUES
    (ws, dach_team_id, admin_id),
    (ws, dach_team_id, rep_id)
  ON CONFLICT (team_id, user_id) DO NOTHING;

  -- A seat with no role_assignment has NO permissions — every object check
  -- (pipeline.read, deal.read, …) fails closed, so Rep One can't even load a
  -- list, let alone see a record shared with them. Assign the 'rep' system role
  -- (seeded at workspace bootstrap): team-scoped read/write, so Rep One sees the
  -- team's records plus whatever is explicitly shared. Idempotent via NOT EXISTS
  -- (role_assignment's uniqueness is an expression index over COALESCE(team_id)).
  INSERT INTO role_assignment (workspace_id, role_id, user_id)
  SELECT ws, r.id, rep_id
    FROM role r
    WHERE r.workspace_id = ws AND r.key = 'rep'
      AND NOT EXISTS (
        SELECT 1 FROM role_assignment ra
        WHERE ra.user_id = rep_id AND ra.role_id = r.id AND ra.team_id IS NULL
      );

  -- An own-scoped counterpart to the team-scoped 'rep': identical object reach,
  -- narrower row scope, so a holder sees ONLY their own records plus whatever is
  -- explicitly shared with them. Cloned from 'rep' (object grants stay in
  -- lockstep) with row_scope overridden to 'own'. Not a system role — it exists
  -- so the individual demo seat (Rep Two, below) makes record sharing OBSERVABLE:
  -- with no team and no owned records, a grant is the sole reason a record shows.
  INSERT INTO role (workspace_id, key, name, is_system, permissions)
  SELECT ws, 'individual', 'Individual (own records)', false,
         jsonb_set(r.permissions, '{row_scope}', '"own"'::jsonb)
    FROM role r
    WHERE r.workspace_id = ws AND r.key = 'rep'
  ON CONFLICT (workspace_id, key) DO NOTHING;

  -- Rep Two: an individual contributor — own-scoped, in NO team. Contrast with
  -- Rep One (team-scoped, in DACH Sales): Rep One sees the team's records by
  -- scope, Rep Two sees nothing until a record is explicitly shared with them.
  INSERT INTO app_user (workspace_id, email, password_hash, display_name, seat_type, status)
  VALUES (ws, 'rep2@demo.test', admin_hash, 'Rep Two', 'full', 'active')
  ON CONFLICT (workspace_id, lower(email)) DO NOTHING;

  SELECT id INTO rep2_id
    FROM app_user
    WHERE workspace_id = ws AND lower(email) = lower('rep2@demo.test');

  INSERT INTO role_assignment (workspace_id, role_id, user_id)
  SELECT ws, r.id, rep2_id
    FROM role r
    WHERE r.workspace_id = ws AND r.key = 'individual'
      AND NOT EXISTS (
        SELECT 1 FROM role_assignment ra
        WHERE ra.user_id = rep2_id AND ra.role_id = r.id AND ra.team_id IS NULL
      );

  RAISE NOTICE 'seed-dev.sql: rep@demo.test (rep, team DACH Sales) + rep2@demo.test (individual, own-scope) seeded for demo-workspace';
END $$;

COMMIT;
