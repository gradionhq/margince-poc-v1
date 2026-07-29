-- CAP-DDL-4 (ADR-0063): the running page's live tally.
--
-- A run's counters advance once per COMMITTED page, and a page is a hundred
-- messages of provider I/O and capture work — a minute and a half in practice.
-- The activation view therefore sat at "queued, 0, 0, 0" for the whole first
-- page, which is indistinguishable from an import that never started.
--
-- These columns hold what the page running RIGHT NOW has seen. They are
-- advisory and transient, never authority: the status read adds them to the
-- committed counters, and every write that ends a page — commit, transient
-- fault, terminal failure, cancel — resets them to zero in the same
-- statement. That is what keeps a retried page honest: the committed columns
-- still move only at commit, so a page walked twice is counted once.
-- Every counter the status read reports has a twin here, so the numbers a
-- caller sees still reconcile mid-page: scanned - captured = skipped holds
-- while the page runs, not only after it commits.
ALTER TABLE capture_backfill
  ADD COLUMN inflight_scanned       int NOT NULL DEFAULT 0,
  ADD COLUMN inflight_captured      int NOT NULL DEFAULT 0,
  ADD COLUMN inflight_skipped       int NOT NULL DEFAULT 0,
  ADD COLUMN inflight_people        int NOT NULL DEFAULT 0,
  ADD COLUMN inflight_organizations int NOT NULL DEFAULT 0;
