# ADR-0063 — Mailbox backfill is bounded, previewed before it spends, and steady-state sync stays incremental

**Status:** Active
**Decided:** 2026-07-18

## The decision

Connecting a mailbox may pull history once, over a window the user picks
from a closed set. It is not a calendar-year rule and not "everything" — the
set is measured in months, and re-invoking with a larger window is allowed
because the capture key makes the overlap a no-op. The user must preview
first: a preview call returns the provider-side message count for the window
plus the projected AI cost, and only an explicit start consumes it. After
that one run, sync stays incremental from provider deltas. A transient
provider failure never kills a connection: rate limits honour `Retry-After`,
other transient errors back off exponentially, and a connection that has
failed persistently is probed daily rather than abandoned.

## Why

Inference runs on the customer's own key, so a backfill over months of mail
is a real bill. An unbounded window would be an unpreviewable spend and an
unbounded draw on the provider's quota. The picker is where the customer
consents to the cost. Separately, a nightly sweep with no per-connection
state had no way to recover: one rate-limit response flipped a connection to
`error` and silently dropped it from the sweep forever.

## What it binds in this repository

- `backend/internal/modules/capture/backfill.go` holds the one statement of
  the window set: `backfillWindowMonths = []int{3, 6, 12, 24, 60}` months,
  exported as `BackfillWindowMonths()`. Choosing no backfill means never
  starting a run.
- `backend/backfillwindow_test.go` runs
  `TestTheBackfillWindowSetIsOneSet`, which pins the contract enums and the
  database check constraint against that Go slice, so a widening cannot
  reach one and miss the other.
- `backend/migrations/core/0092_capture_backfill.up.sql` creates
  `capture_backfill`: one live run per connection, its own backward-paging
  provider cursor separate from the forward-moving sync cursor, a frozen
  `after_date` computed at start, and counter columns
  (`scanned`, `captured`, `skipped`, `people_created`,
  `organizations_created`, `dedupe_candidates`) that make the activation
  screen a single indexed row read. A worker death resumes from the last
  committed cursor; cancelling retains the rows already captured.
- `backend/migrations/core/0250_backfill_window_reach.up.sql` widened the
  check constraint to the current five values.
- `backend/api/crm.yaml` carries `/connectors/{provider}/backfill/preview`,
  `/connectors/{provider}/backfill` for start and status, and the cancel
  operation. The preview needs a live provider round-trip, so a provider
  outage surfaces honestly as a 502 rather than a fabricated estimate.
- `backend/migrations/core/0087_capture_sync_state.up.sql` creates
  `capture_sync_state`, the per-connection scheduling sidecar:
  `next_sync_at`, `consecutive_failures`, `last_synced_at`,
  `last_success_at` and `last_error_class` (one of `rate_limited`,
  `unreachable`, `auth`, `history_gone`, `internal`). Error detail stays in
  `system_log`; the row holds only the class.
- Backoff is 2 minutes doubled per failure, capped at 4 hours, jittered.
  `backend/internal/modules/capture/syncstate.go` drives it and writes a
  `capture_sync_error` row to `system_log`.
- The recurring passes are declared in `backend/api/jobs.yaml`:
  `capture_sync` for the incremental pull, `capture_backfill` for the paged
  history run, `capture_classify` hourly, and the daily passes
  `capture_enrich`, `capture_auto_enrich_sweep` and `capture_digest`. Each
  dispatcher fans out one job per workspace.

## History

Adopted from the retired specification, decided 2026-07-18. Rewritten in
plain language 2026-08-19.

Two things have moved since the source. The window set was three, six and
twelve months; it now also offers twenty-four and sixty, widened by
ADR-0106. The source also pins a single ordered nightly suite that runs
catch-up sync, classify, reconcile, enrich, the dedupe sweep and the digest
in sequence; no such dispatcher exists. The passes are separate declared
jobs with their own cadences, and `capture_enrich` says so in its own
reason text.
