-- 0065: the formula-field boundary proof (RD-T08, RD-AC-6/RD-AC-7/RD-AC-N-1):
-- a formula field is a database-GENERATED artifact, never a runtime-authored
-- expression. Two concrete examples:
--
-- 1. Same-row GENERATED column (RD-AC-6): deal.amount_minor_base, the
--    base-currency-converted amount computed from the deal's own
--    amount_minor x fx_rate_to_base — roll-ups must aggregate the
--    base-currency value, never a raw native amount_minor across
--    currencies. GENERATED ALWAYS AS ... STORED mirrors deal.search_tsv's
--    existing GENERATED column (0006_deals.up.sql) in style. A NULL input
--    (either amount_minor or fx_rate_to_base still unset) yields a NULL
--    result: Postgres evaluates the expression per row, and ordinary
--    NULL-propagating arithmetic already gives the honest
--    "not-computable-yet" state for free, no CASE needed. An open deal
--    commonly has fx_rate_to_base still NULL (deal_closed_fx only requires
--    it once the deal transitions off 'open'), so this is a real, not a
--    contrived, missing-input case.
--
-- 2. Cross-record aggregate (the RD-AC-N-1 reconciliation): a same-row
--    GENERATED column structurally cannot read other rows (a Postgres
--    limitation), so the per-organization open-pipeline roll-up is served
--    as a security_invoker VIEW — it runs with the CALLING role's
--    privileges and RLS, not the view owner's, so it inherits deal's
--    tenant-isolation policy (0014_rls.up.sql) exactly as if the base
--    table were queried directly; no new grant is needed beyond what
--    0015 already extends to every future table-shaped object the owner
--    creates (views included). It reads only existing columns
--    (amount_minor_base, status, organization_id, archived_at) — no new
--    tables, never a runtime interpreter. An organization with no open
--    deals produces no row at all (never a fabricated zero); an
--    organization with open deals whose amount_minor_base is itself not
--    yet computable (missing FX input) still produces a row, with the
--    aggregate column NULL (SUM ignores NULLs) — both are the honest
--    "not computable yet" state, distinct from a genuine zero-value
--    roll-up.
ALTER TABLE deal ADD COLUMN amount_minor_base bigint
  GENERATED ALWAYS AS (round(amount_minor * fx_rate_to_base)::bigint) STORED;

CREATE VIEW organization_open_pipeline_rollup
  WITH (security_invoker = true) AS
    SELECT
      d.organization_id,
      sum(d.amount_minor_base) AS open_pipeline_minor_base,
      count(*)                 AS open_deal_count
    FROM deal d
    WHERE d.status = 'open'
      AND d.organization_id IS NOT NULL
      AND d.archived_at IS NULL
    GROUP BY d.organization_id;
