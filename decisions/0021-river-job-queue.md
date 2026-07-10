# 0021 — River for discrete background jobs (worklist §1c platform seam)

Date: 2026-07-10. Implements the River arm of the platform-parity push
ratified in the 2026-07-10 founder walkthrough
([docs/worklists/skeleton-baseline-2026-07-09.md](../docs/worklists/skeleton-baseline-2026-07-09.md)
§0b, §1c). Blobstore and keyvault are the sibling seams of the same push,
each with its own ADR. This record ratifies the design; the concrete
code steps are in
[docs/worklists/river-worker-migration-2026-07-10.md](../docs/worklists/river-worker-migration-2026-07-10.md).

## Context

`cmd/worker` today runs its scheduled passes as bare Go ticker loops. Two
of them are in scope for this decision:

- `--close-date-interval` (default 24h) → `deals.RunCloseDateSweep`, which
  ticks `CloseDateCorrector.Sweep` (INV-CLOSE-PAST hygiene).
- `--reconcile-interval` (default 24h) → `deals.RunFollowUpReconcile`, which
  ticks `FollowUpReconciler.Reconcile` (overnight follow-up proposals,
  features/07 §8a).

Both share one shape (`cmd/worker/main.go`): a `time.NewTicker`, a body
that logs-and-forgets on error, and a `select` on `ctx.Done()`. The domain
work is a single method (`Sweep(ctx)` / `Reconcile(ctx)`) that iterates
every live workspace under its own GUC.

That shape has four latent problems, all of which bite at fleet scale:

1. **No leader election.** Running two `cmd/worker` replicas double-runs
   every sweep — two overnight reconcilers stage the same follow-up
   proposals against the same deals concurrently. Nothing today prevents
   this; horizontal scaling of the worker role is a silent correctness bug.
2. **No retry / backoff.** A pass that fails (transient DB blip, a bad
   workspace) is logged once and dropped until the next 24h tick. There is
   no record it ever ran or failed beyond a log line.
3. **In-flight work is lost on shutdown.** A `SIGTERM` mid-`Sweep` abandons
   the pass; the ticker's `<-ctx.Done()` returns and the partial pass leaves
   no durable trace of where it stopped.
4. **No observability or dead-letter.** "Did the reconciler run last night,
   and what happened?" is answerable only by grepping logs.

The transactional outbox (`platform/events`, storekit `Emit`) does **not**
solve this: the outbox propagates *events* (something happened, tell the
bus), at-least-once, via Redis Streams and consumer groups. It is not a job
scheduler and has no notion of "run this on a cadence, retry it, and only
once cluster-wide." Bending it into one would corrupt the event write-shape,
which is core and non-negotiable.

## Decision

**Adopt [River](https://riverqueue.com) (`github.com/riverqueue/river`,
`riverpgxv5` driver) as the durable job substrate for discrete background
work, scoped in this pass to replace the close-date and reconcile ticker
loops, and to be the declared home for future discrete jobs.**

River is chosen over the custom loops for the long term because it supplies,
Postgres-native and with no new infrastructure (it rides the existing
`pgxpool`), exactly the four things the ticker lacks: **leader election**
(only one client in the fleet runs a periodic job — `river_leader`),
**retries with backoff**, **graceful drain** of in-flight jobs on shutdown,
and **observability + dead-letter** (every attempt is a queryable
`river_job` row; exhausted jobs land `discarded`, not lost). It also gives
**uniqueness** (a slow sweep cannot stack a second run on top of itself),
which reproduces the ticker's implicit "one pass at a time" and hardens it
across replicas.

### The outbox / River boundary (the load-bearing rule)

These two substrates are complementary and MUST NOT be conflated:

| | **Outbox** (`platform/events`) | **River** (`platform/jobs`) |
|---|---|---|
| Answers | "Something happened — tell the world." | "Do this piece of work." |
| Unit | An event envelope (events.md §2) | A job (args + a worker) |
| Written | Inside the domain tx, via storekit `Emit` | Enqueued by a schedule (periodic) or a caller |
| Delivery | At-least-once via Redis Streams + consumer groups + `Dedupe` | At-least-once via `river_job`, retried with backoff, unique-guarded |
| Status | Fire-and-forget; the relay ships it | Durable lifecycle: available→running→completed/discarded |

The rule: **an event announces a fact (outbox); a job requests work
(River).** River sits *above* the outbox — a job's work may itself commit
domain rows and `Emit` outbox events (the sweep stages approvals, which
emit). River never carries domain events, and the outbox never schedules
jobs. **The transactional outbox is untouched by this decision.**

### Scope discipline

- **In scope, now:** close-date sweep and follow-up reconcile become River
  **periodic jobs**. The domain methods `Sweep`/`Reconcile` are unchanged —
  only their *scheduler* changes from a Go ticker to a River periodic job.
- **Explicitly out of scope this pass, natural follow-ons:** the retention
  evaluator (`--retention-interval`, a GDPR engine) and the Surface-B runner
  scheduler (`--runner-interval`) stay ticker-driven for now; River is the
  home they can migrate to when touched. The `cg:*` event subscribers
  (context-graph embeddings, workflows, resume) stay outbox subscribers —
  they are event consumers, not jobs.

### Composition and layout

- **`internal/platform/jobs`** (new) — the River client lifecycle chassis,
  the peer of `platform/events` for the outbox: it owns the
  `river.Client[pgx.Tx]` construction over the shared pool, `Start`/`Stop`,
  logger wiring, and the queue config. It owns no domain.
- **`internal/compose/jobs.go`** — the cross-cutting wiring point (ADR-0054:
  every cross-module/infra edge is injected in compose). The job args and
  the River worker adapters live here; each adapter is a thin
  `river.Worker` that delegates to the existing `deals` correctors
  (`compose.NewCloseDateCorrector` / `NewFollowUpReconciler`). **The `deals`
  module gains no River import** — its `Sweep`/`Reconcile` seam stays the
  stable, River-agnostic boundary, which is what makes the swap
  behavior-preserving.
- **`cmd/worker`** — constructs and `Start`s the job runner instead of
  launching the two ticker goroutines. The `--close-date-interval` /
  `--reconcile-interval` flags (and their 24h defaults) are **kept** as the
  periodic-job schedule source, so the operator-facing config surface does
  not change. River workers run **only in `cmd/worker`**, exactly as the
  ticker loops do today — a small install running `cmd/api --inline-relay`
  gets the outbox relay but not the sweeps, unchanged.
- The now-unused `deals.RunCloseDateSweep` / `RunFollowUpReconcile` ticker
  wrappers are deleted (no dead code — T3).

### Schema / migration ownership

River owns its own schema through its own migrator
(`river/rivermigrate`): `river_job`, `river_leader`, `river_queue`,
`river_client`, `river_client_queue`, `river_migration`. `cmd/migrate`
runs `rivermigrate` up as a **fourth namespace step** after the core and
custom SQL namespaces (ADR-0017 order preserved). This is consistent with
both ADR-0017 (River is one self-contained namespace with its own tracking
table, `river_migration`) and ADR-0002 — golang-migrate was rejected for
*our* schema because our three namespaces would need three instances; River
is precisely the "one migrator for one self-owned namespace" case that
objection does not touch. Upgrading River upgrades its schema through the
library, not through a hand-copied SQL file we would have to keep in sync.

River's tables carry **no `workspace_id`**: they are global operational
infrastructure, accessed by the library through the pool without a GUC —
the correct posture, since scheduling is not tenant data. The per-tenant
isolation contract is untouched: the job's `Work(ctx)` still calls
`Sweep`/`Reconcile`, which still open `WithWorkspaceTx` per workspace. The
tenant-table fitness gates (RLS-FORCE, table-ownership) get River's tables
added to their operational-infra allowlist alongside `schema_migrations_*`.

## Consequences

- **Correctness win:** the worker role becomes safe to scale horizontally —
  River's leader election guarantees each periodic job runs once
  cluster-wide. This closes the double-run bug that exists today.
- **Durability win:** shutdown drains in-flight jobs; a failed pass retries
  with backoff and is inspectable in `river_job` rather than lost to a log
  line. This is strictly better than the ticker, not merely equivalent.
- **New dependency:** `github.com/riverqueue/river` (+ `riverpgxv5`,
  `rivermigrate`) enters `go.mod`. It is pgx-v5-native and Go-1.26-clean.
  Image-pin and SonarCloud posture unaffected (pure Go, Postgres-backed).
- **Behavior preservation is provable:** because the domain seam
  (`Sweep`/`Reconcile`) does not change, the existing
  `closedate_integration_test.go` / `reconcile_integration_test.go` (which
  drive those methods directly) keep proving the domain behavior verbatim. A
  new compose integration test proves the River wiring — periodic
  enqueue → leader-elected execution → the same staged outcome, plus retry,
  uniqueness, and drain — against real Postgres. `RunOnStart` on the periodic
  jobs preserves the ticker's boot-time first pass.
- **Blast radius is small:** `deals` unchanged; `platform/jobs` new;
  `compose` and `cmd/worker`/`cmd/migrate` wire it. No contract, no HTTP
  surface, no outbox change.
