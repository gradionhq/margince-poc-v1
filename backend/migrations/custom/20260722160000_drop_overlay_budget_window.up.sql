-- 20260722160000_drop_overlay_budget_window: remove the PG-backed OVB
-- budget-window table (branch 1b, A3b). The overlay budget meter is now
-- Redis-backed with static YAML config per the ratified overlay-budget
-- chapter ("Redis counters plus static YAML config: no migration, no UI,
-- no contract change") — the platform/overlaybudget package. The
-- Postgres table it briefly used (20260722100000) is superseded; drop it
-- rather than edit the shipped migration in place (additive-only).
DROP TABLE IF EXISTS overlay_budget_window;
