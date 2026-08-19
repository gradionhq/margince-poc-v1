# ADR-0064 — AI metering and captured payloads live in two tables, not one

**Status:** Active
**Decided:** 2026-07-18

## The decision

Every model call writes one row to `ai_call`: the task, the tier, the token
counts, whether the response was cached, and the run that produced it. That row
is telemetry, so it carries no audit or outbox ride-along and no provenance
columns — a meter reading is not a claim about a customer record. It is long-
lived, because there is no content in it to age out.

The request and response text goes to a separate table, `ai_call_payload`, and
only when an operator explicitly turns capture on. Capture is off by default.
The payload table is retention-aged, is reached by the erasure cascade, and is
deleted with its call by a cascading foreign key. A purged call takes its
payload with it; a purged payload never touches the metering row.

## Why

The two kinds of data have different lifetimes and different sensitivity, and
one table cannot serve both. Spend history is permanent operational telemetry
the customer's own bill depends on. Captured text is whatever the customer's
users typed, which can include anything, so it must be able to disappear on its
own schedule without taking the spend history with it. Collapsing them means
either the meter inherits an aging it does not need, or the content escapes
aging by riding a table that is never aged.

Off by default is load-bearing rather than a preference. Captured text may hold
special-category personal data, so capturing nothing is the safer starting
state.

## What it binds in this repository

- `backend/migrations/core/0088_ai_call.up.sql` creates `ai_call`;
  `0089_ai_call_payload.up.sql` creates `ai_call_payload` with the cascade.
- `ai_call` carries `agent_run_id`, so an agent run's calls are found by a join
  rather than by a second table.
- The capture switch is the deployment setting `ai.capture_payloads`, read
  through `backend/internal/platform/deployconfig/deployconfig.go`. The
  contract exposes it to a client as `payload_capture_enabled` so the client
  can tell "capture is off" from "nothing was captured".
- Writing and reading the meter is `backend/internal/modules/ai/callstore.go`,
  `callread.go` and `callstats.go`.
- The erasure path deletes payload rows in
  `backend/internal/modules/privacy/erasuretimeline.go`; the 365-day retention
  erase on `ai_call_payload` ages the rest.
  `backend/internal/modules/privacy/erasure_channels.go` records why the
  payload table gets no per-channel lane.
- Later migrations extend the meter without merging the two tables:
  `0100_ai_call_attempts`, `0102_ai_call_company_context`,
  `0106_ai_call_terminal_trace_index`, `0147_drop_ai_call_stored_cost`.

## History

Adopted from the retired specification, decided 2026-07-18. Rewritten in plain
language 2026-08-19.
