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
-- Currently seeds: FX rates (fx_rate is an exchange-rate feed — no API, no
-- audit_log/event_outbox, and the product never invents a rate at runtime, so a
-- non-EUR deal cannot be won without a seeded rate). Rates are seeded ONLY for
-- the demo workspace here, never at workspace bootstrap, so real workspaces keep
-- the honest "no rate → 422, never rate=1" behaviour.
--
-- Requires the compose stack's Postgres (make seed-dev-db runs it as
-- margince_owner; make dev applies it over the dev owner DSN).

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

COMMIT;
