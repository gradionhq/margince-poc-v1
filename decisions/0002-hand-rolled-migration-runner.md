# 0002 — Hand-rolled migration runner instead of golang-migrate/goose/Atlas

Status: accepted · 2026-07-03

The backlog leaves the migration tool open ("an implementation call
recorded in an ADR", B-EP02.1a). We ship `internal/pgmigrate` (~200 lines,
stdlib + pgx) rather than adopting golang-migrate.

Why: ADR-0017 requires **three ownership namespaces** — sequential
upstream `core/`, timestamp fork-owned `custom/`, and per-jurisdiction
`schema_migrations_<code>` — each with its own tracking table and a fixed
core-then-custom-then-packs apply order. golang-migrate models exactly one
directory + one table per instance; satisfying ADR-0017 means running
three configured instances and hand-coordinating their order anyway, at
the cost of a large dependency. Atlas is out per ADR-0017 F-T5 (the
column-pattern `exclude` its drift-guard would need is refuted).

Shape: `.up.sql`/`.down.sql` pairs (a missing down is a load error —
every migration must reverse, B-EP02.1b), one transaction per migration
including its tracking row, a cluster-wide advisory lock, idempotent
re-runs. The apply/reverse/re-apply property is integration-tested against
real Postgres 16.

Revisit when: jurisdiction packs land (the third namespace is designed in
but unexercised), or if the runner grows features a maintained library
already has (dirty-state recovery, out-of-order detection).
