# ADR-0084 — A connector backfill audits once per run; an incremental sync audits per changed row

**Status:** Active — the content-hash rule and the silent no-op are built.
The per-run backfill audit and the per-row sync audit are **not built**; see
below.

**Decided:** 2026-08-06

## The decision

A connector that mirrors an external system does two different jobs under one
name, and each takes a different write shape.

The first pull after a source is connected is a backfill. It is one operation
a human performed with one click, so it writes exactly one audit row naming
the actor, the connection, the cursor range and the counts, and emits exactly
one completion event. It does not audit or emit per record. A resumed backfill
continues the same run identity, so an interrupted one still produces one audit
row.

Everything after that is incremental sync. A record whose source content
actually changed is a mutation and takes the full house write shape: the
domain row, its audit row and its outbox event, in one transaction.

A record whose content did not change writes nothing at all — no row update,
no audit row, no event, no version bump. Every mirrored row stores a hash of
the source content it was built from, and an upsert whose hash matches the
stored hash is silent. The hash covers the source's own values only, never a
derived one.

The connection's own state decides which mode applies. A connection is in
backfill until its first full pass records a cursor, and incremental from then
on. There is no operator switch.

## Why

Both halves break if you force them into one shape. A backfill that audits per
record turns ten thousand historical invoices into twenty thousand extra rows
and floods the relay with events nobody subscribes to, for one click. An
incremental sync that only writes a run-level summary loses the moment an
invoice flipped to disputed, which is exactly the question an audit log exists
to answer.

The derived-value trap is the one that bites silently. An open invoice becomes
overdue at midnight with nobody touching it, so a hash including status would
make every morning's pass rewrite every row and report change where none
happened. Status is recomputed on read instead.

## What it binds in this repository

- `backend/internal/modules/finance/sync.go` is the first application. Its
  `hashOf` is the change key, computed over the source's own values only.
- `backend/internal/modules/finance/mirror.go` short-circuits on a matching
  `sync_hash`, so a pass over an unchanged ledger writes nothing.
- `SyncResult.Unchanged` in `sync.go` counts the silent case.
- The `sync_hash` column sits on `finance_invoice` and
  `finance_external_customer`, created in
  `backend/migrations/core/0202_finance_mirror.up.sql`. The customer upsert
  carries `WHERE finance_external_customer.sync_hash <> EXCLUDED.sync_hash`,
  so an unchanged row is not rewritten by the database either.
- `backend/internal/modules/finance/sync_integration_test.go` asserts that a
  row nobody touched keeps its version.
- `backend/internal/platform/database/storekit` holds the one spelling of the
  audit-plus-outbox write shape that the sync half is meant to call.

## What is not built

The finance mirror writes no audit rows and emits no outbox events at all, in
either mode. It calls `storekit.CapturedBy` and `storekit.LockRow` but never
`storekit.Audit` or `storekit.EmitEvent`. The doc comment at the top of
`sync.go` claims the house write shape is used, and that claim is currently
wrong.

The backfill and incremental modes are also not distinguished at runtime. The
`sync_cursor` column exists on `finance_connection` and its comment describes
the resumable run, but no Go code reads or writes it, so every pass behaves
the same way.

What is owed is the audit half of both modes: one run-level audit row and one
completion event for a backfill, and the full per-row shape for a changed
record during incremental sync. The design for the run record — where the
counts and the cursor range are stored — is not final. Until that lands, a
mirrored invoice changing state leaves no audit trail, and the fix should
correct the `sync.go` doc comment in the same change.

## History

Adopted from the retired specification, decided 2026-08-06. Rewritten in plain
language 2026-08-19. The source is marked proposed.

The source generalizes the rule to every connector and names the overlay mirror
as a second case. The overlay module audits its connection lifecycle —
connect, activate, flip — through `storekit.Audit`, but not its mirrored
rows, so the same gap holds there.
