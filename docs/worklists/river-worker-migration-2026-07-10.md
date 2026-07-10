# River worker-loop migration — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `cmd/worker`'s `--close-date-interval` and
`--reconcile-interval` Go ticker loops with River periodic jobs, leaving the
domain logic (`Sweep`/`Reconcile`) and the transactional outbox untouched.

**Architecture:** River (`riverqueue/river`, `riverpgxv5` driver) rides the
existing `pgxpool`. A new `platform/jobs` package owns the River client
lifecycle (the peer of `platform/events`); `compose/jobs.go` holds the job
args and thin worker adapters that delegate to the existing `deals`
correctors; `cmd/worker` starts the runner instead of launching tickers;
`cmd/migrate` runs River's own migrator as a fourth namespace step. The
`deals` module gains no River import — its `Sweep`/`Reconcile` methods are
the stable, River-agnostic seam, which is what makes the swap
behavior-preserving.

**Tech Stack:** Go 1.26.5, pgx v5.10.0, `github.com/riverqueue/river` (+
`riverdriver/riverpgxv5`, `rivermigrate`), Postgres 16.

**Decision of record:** [decisions/0021-river-job-queue.md](../../decisions/0021-river-job-queue.md).

## Global Constraints

- Go **1.26.5**; pgx **v5.10.0** (`github.com/jackc/pgx/v5`). River must be
  pinned in `go.mod`; run `make tools`/`go mod tidy` and commit `go.sum`.
- Every new hand-written `*.go` starts with the two-line BUSL-1.1 SPDX
  header (enforced by `backend/license_test.go`). Not on `*_gen.go`.
- Craft gate (diff-scoped, pre-push, BLOCKER): **never swallow an error**,
  **no `time.Sleep`/real-clock/real-network in tests**. Wait on River's
  completion subscription channel with a context deadline — never sleep.
- Integration tests are `//go:build integration` and gate on the
  `MARGINCE_TEST_*` env contract; a skipped integration test fails the lane
  (`make test-integration`).
- Non-test, non-generated Go files stay < 500 LOC (`go-file-length` gate).
- Commits signed off (`git commit -s`); `PATH="$(go env GOPATH)/bin:$PATH" make check`
  green locally before push; `make test-integration` green (needs `make db-up`).
- Scope is **exactly** the two named loops. Do **not** touch the retention
  evaluator, the Surface-B runner scheduler, the `cg:*` event subscribers,
  or the outbox relay.

## File Structure

- **Create** `backend/internal/platform/jobs/jobs.go` — `Runner` wrapping
  `river.Client[pgx.Tx]`: `New`, `Start`, `Stop`. Owns no domain.
- **Create** `backend/internal/platform/jobs/jobs_test.go` (`//go:build integration`)
  — proves a client boots against real PG and stops cleanly.
- **Create** `backend/internal/compose/jobs.go` — job args
  (`CloseDateSweepArgs`, `FollowUpReconcileArgs`), the two `river.Worker`
  adapters delegating to `deals` correctors, and `NewJobRunner`.
- **Create** `backend/internal/compose/jobs_test.go` — unit: `NewJobRunner`
  registers both periodic jobs with the expected kinds and intervals (no DB).
- **Create** `backend/internal/compose/jobs_integration_test.go`
  (`//go:build integration`) — the behavior-preservation proof.
- **Modify** `backend/cmd/migrate/main.go` — run `rivermigrate` up/down as
  the fourth namespace step.
- **Modify** `backend/cmd/worker/main.go` — construct + `Start` the runner;
  delete the two `background.Go(deals.Run…)` launches.
- **Modify** `backend/internal/modules/deals/closedatesweep.go`,
  `backend/internal/modules/deals/reconcile.go` — delete the now-unused
  `RunCloseDateSweep` / `RunFollowUpReconcile` ticker wrappers.
- **Modify** `backend/tableownership_test.go` (and the RLS-FORCE fitness
  test, wherever it enumerates tenant tables) — allowlist River's tables as
  operational infra.
- **Modify** `backend/go.mod`, `backend/go.sum` — River deps.
- **Modify** `.env.template`, `docs/reference/configuration.md` — no new
  flags (intervals are kept), but note River's schema is applied by
  `make migrate`.

---

## The four checkpoint questions, answered up front

**1. Which River queues / periodic-jobs replace the loops?**
One queue, `river.QueueDefault` (low volume, 24h cadence). Two periodic
jobs on it:

| Old loop | River periodic job (Kind) | Delegates to | Schedule source |
|---|---|---|---|
| `RunCloseDateSweep` (`--close-date-interval`) | `close_date_sweep` | `deals.CloseDateCorrector.Sweep` | `--close-date-interval` (default 24h) |
| `RunFollowUpReconcile` (`--reconcile-interval`) | `follow_up_reconcile` | `deals.FollowUpReconciler.Reconcile` | `--reconcile-interval` (default 24h) |

Each periodic job is registered with `RunOnStart: true` (preserves the
ticker's boot-time first pass) and `UniqueOpts{ByState: UniqueStatesDefault}`
(a slow pass cannot stack a second run — reproduces the ticker's implicit
one-pass-at-a-time, now enforced across replicas).

**2. What schema / migration does River need?**
River's own migrator (`rivermigrate`) creates `river_job`, `river_leader`,
`river_queue`, `river_client`, `river_client_queue`, `river_migration`. No
`workspace_id` (global operational infra). `cmd/migrate` runs it as the
fourth step after core + custom (ADR-0017 order preserved). Upgrades ride
the library, not a hand-copied SQL file.

**3. How does it compose through `internal/compose`?**
`platform/jobs` owns the River client lifecycle. `compose/jobs.go` owns the
args + the worker adapters (delegating to `compose.NewCloseDateCorrector` /
`NewFollowUpReconciler`) + `NewJobRunner`, which builds the `river.Workers`
registry and `PeriodicJobs` list and hands them to `jobs.New`. `cmd/worker`
calls `NewJobRunner` and `Start`s it. `deals` gains no River import.

**4. How does the integration lane prove the swap is behavior-preserving?**
The existing `closedate_integration_test.go` / `reconcile_integration_test.go`
drive `Sweep`/`Reconcile` **directly** and are unchanged — they keep proving
the domain behavior verbatim, because that seam does not change. A new
`compose/jobs_integration_test.go` proves the River wiring reaches the same
outcome: enqueue → leader-elected execution (observed on River's completion
subscription, no sleep) → the identical staged approval, plus uniqueness and
graceful drain.

---

## Task 1: River dependency + `platform/jobs` client chassis

**Files:**
- Modify: `backend/go.mod`, `backend/go.sum`
- Create: `backend/internal/platform/jobs/jobs.go`
- Test: `backend/internal/platform/jobs/jobs_test.go` (`//go:build integration`)

**Interfaces:**
- Produces:
  - `type Config struct { Queues map[string]river.QueueConfig; Workers *river.Workers; PeriodicJobs []*river.PeriodicJob }`
  - `func New(pool *pgxpool.Pool, cfg Config, log *slog.Logger) (*Runner, error)`
  - `func (r *Runner) Start(ctx context.Context) error`
  - `func (r *Runner) Stop(ctx context.Context) error`

- [ ] **Step 1: Add the dependency**

Run: `cd backend && go get github.com/riverqueue/river@latest github.com/riverqueue/river/riverdriver/riverpgxv5@latest github.com/riverqueue/river/rivermigrate@latest && go mod tidy`
Expected: `go.mod`/`go.sum` updated; `go build ./...` clean.

- [ ] **Step 2: Write the failing test**

```go
//go:build integration

// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package jobs_test

// (uses the repo's integration harness for a real pool + MARGINCE_TEST_* DSN)

func TestRunnerStartsAndStopsCleanly(t *testing.T) {
    pool := integrationPool(t) // repo harness helper
    workers := river.NewWorkers()
    r, err := jobs.New(pool, jobs.Config{
        Queues:  map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 1}},
        Workers: workers,
    }, slog.New(slog.NewTextHandler(io.Discard, nil)))
    if err != nil {
        t.Fatalf("New: %v", err)
    }
    ctx := context.Background()
    if err := r.Start(ctx); err != nil {
        t.Fatalf("Start: %v", err)
    }
    if err := r.Stop(ctx); err != nil {
        t.Fatalf("Stop: %v", err)
    }
}
```

- [ ] **Step 3: Run it — expect FAIL**

Run: `MARGINCE_TEST_DSN=… go test -tags integration ./internal/platform/jobs/ -run TestRunnerStartsAndStopsCleanly -v`
Expected: FAIL — `jobs.New` undefined. (River's schema must exist first —
depends on Task 2 for a green run; the compile-fail here is the red step.)

- [ ] **Step 4: Implement `platform/jobs`**

```go
// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

// Package jobs owns the River client lifecycle — the durable
// background-job substrate, the peer of platform/events for the outbox.
// It owns no domain: workers and periodic jobs are supplied by the
// composition layer. See decisions/0021-river-job-queue.md.
package jobs

import (
    "context"
    "fmt"
    "log/slog"

    "github.com/jackc/pgx/v5"
    "github.com/jackc/pgx/v5/pgxpool"
    "github.com/riverqueue/river"
    "github.com/riverqueue/river/riverdriver/riverpgxv5"
    "github.com/riverqueue/river/rivershared/slogutil" // if needed for the logger adapter
)

// Config is the runner's wiring, populated by the composition layer.
type Config struct {
    Queues       map[string]river.QueueConfig
    Workers      *river.Workers
    PeriodicJobs []*river.PeriodicJob
}

// Runner wraps a River client bound to the shared pool.
type Runner struct {
    client *river.Client[pgx.Tx]
}

func New(pool *pgxpool.Pool, cfg Config, log *slog.Logger) (*Runner, error) {
    client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
        Queues:       cfg.Queues,
        Workers:      cfg.Workers,
        PeriodicJobs: cfg.PeriodicJobs,
        Logger:       log,
    })
    if err != nil {
        return nil, fmt.Errorf("jobs: new client: %w", err)
    }
    return &Runner{client: client}, nil
}

// Start begins working the queues; it returns once startup completes and
// runs until Stop. Errors are surfaced, never swallowed.
func (r *Runner) Start(ctx context.Context) error {
    if err := r.client.Start(ctx); err != nil {
        return fmt.Errorf("jobs: start: %w", err)
    }
    return nil
}

// Stop drains in-flight jobs and shuts the client down gracefully.
func (r *Runner) Stop(ctx context.Context) error {
    if err := r.client.Stop(ctx); err != nil {
        return fmt.Errorf("jobs: stop: %w", err)
    }
    return nil
}
```

Note: confirm the exact slog-logger option River exposes at the pinned
version (`river.Config.Logger` takes a `*slog.Logger`); drop the
`slogutil` import if unused.

- [ ] **Step 5: Run the test — expect PASS** (after Task 2 applies the schema)

Run: `MARGINCE_TEST_DSN=… go test -tags integration ./internal/platform/jobs/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add backend/go.mod backend/go.sum backend/internal/platform/jobs/
git commit -s -m "feat(jobs): River client lifecycle chassis in platform/jobs"
```

---

## Task 2: River migrator in `cmd/migrate` + fitness allowlist

**Files:**
- Modify: `backend/cmd/migrate/main.go`
- Modify: `backend/tableownership_test.go` (+ the RLS-FORCE fitness test)
- Test: `backend/cmd/migrate/main_test.go` if one exists, else the
  `platform/jobs` integration run is the proof the schema applies.

**Interfaces:**
- Consumes: `rivermigrate.New`, `rivermigrate.DirectionUp/DirectionDown`.

- [ ] **Step 1: Add the River migrate step**

In `cmd/migrate/main.go`, after `dbmigrate.Up(ctx, conn, core, custom, …)`
succeeds (up direction), run River's migrator against the pool. River's
migrator wants a pool/driver, not the single `*pgx.Conn` the SQL runner
uses — open a short-lived `pgxpool` (or reuse the owner DSN) for it:

```go
// River owns its own schema through its own migrator (rivermigrate),
// applied as the fourth namespace after core+custom (ADR-0017 order).
// decisions/0021-river-job-queue.md.
migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
if err != nil {
    return fmt.Errorf("migrate: river migrator: %w", err)
}
res, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil)
if err != nil {
    return fmt.Errorf("migrate: river up: %w", err)
}
_, _ = fmt.Fprintf(stdout, "river: applied %d migration(s)\n", len(res.Versions))
```

**`down` is deliberately NOT mirrored.** Folding River into the shared
`--steps` counter would let a routine `migrate down --steps 1` — meant to
undo the last custom/core migration — silently drop a River migration and
break the job infrastructure. River rollback is a separate, explicit,
rarely-needed operation; the `down` subcommand keeps reverting only the SQL
namespaces (custom then core). This matches the ADR and the shipped
`cmd/migrate`.

- [ ] **Step 2: Update the tenant-table fitness allowlist**

River's tables have no `workspace_id`. Add them to whatever list marks
operational (non-tenant) infra — the same posture as `schema_migrations_*`:

```go
// River owns these; they are global job-queue infra, not tenant data —
// no workspace_id, so they are outside the RLS-FORCE and table-ownership
// contracts by design. decisions/0021.
var operationalTables = map[string]bool{
    "river_job": true, "river_leader": true, "river_queue": true,
    "river_client": true, "river_client_queue": true, "river_migration": true,
}
```

Wire this skip into both the table-ownership walk and the RLS-FORCE
enumeration so a River table can never be mistaken for an un-owned or
un-protected tenant table.

- [ ] **Step 3: Prove the schema applies and the gates stay green**

Run: `make db-up && make migrate` — expect `river: applied N migration(s)`.
Run: `MARGINCE_TEST_DSN=… go test -tags integration ./... -run TableOwnership -v`
and the RLS-FORCE test — expect PASS (River tables skipped, not flagged).

- [ ] **Step 4: Commit**

```bash
git add backend/cmd/migrate/main.go backend/tableownership_test.go backend/*_test.go
git commit -s -m "feat(migrate): apply River schema; exempt its tables from tenant-table gates"
```

---

## Task 3: compose job args, worker adapters, and `NewJobRunner`

**Files:**
- Create: `backend/internal/compose/jobs.go`
- Test: `backend/internal/compose/jobs_test.go` (unit, no DB)

**Interfaces:**
- Consumes: `compose.NewCloseDateCorrector(pool, log) *deals.CloseDateCorrector`,
  `compose.NewFollowUpReconciler(pool, log) *deals.FollowUpReconciler`,
  `jobs.New`, `jobs.Config`.
- Produces:
  - `type CloseDateSweepArgs struct{}` with `Kind() string` → `"close_date_sweep"`
  - `type FollowUpReconcileArgs struct{}` with `Kind() string` → `"follow_up_reconcile"`
  - `func NewJobRunner(pool *pgxpool.Pool, log *slog.Logger, closeDateInterval, reconcileInterval time.Duration) (*jobs.Runner, error)`

- [ ] **Step 1: Write the failing unit test**

```go
// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

func TestCloseDateSweepArgsKind(t *testing.T) {
    if got := CloseDateSweepArgs{}.Kind(); got != "close_date_sweep" {
        t.Fatalf("Kind() = %q, want close_date_sweep", got)
    }
}

func TestFollowUpReconcileArgsKind(t *testing.T) {
    if got := FollowUpReconcileArgs{}.Kind(); got != "follow_up_reconcile" {
        t.Fatalf("Kind() = %q, want follow_up_reconcile", got)
    }
}
```

- [ ] **Step 2: Run — expect FAIL** (`undefined: CloseDateSweepArgs`)

Run: `go test ./internal/compose/ -run Kind -v`
Expected: FAIL to compile.

- [ ] **Step 3: Implement `compose/jobs.go`**

```go
// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

import (
    "context"
    "log/slog"
    "time"

    "github.com/jackc/pgx/v5/pgxpool"
    "github.com/riverqueue/river"

    "github.com/gradionhq/margince/backend/internal/platform/jobs"
)

// CloseDateSweepArgs schedules one close-date hygiene pass (INV-CLOSE-PAST).
type CloseDateSweepArgs struct{}

func (CloseDateSweepArgs) Kind() string { return "close_date_sweep" }

// FollowUpReconcileArgs schedules one overnight follow-up reconciliation
// pass (features/07 §8a).
type FollowUpReconcileArgs struct{}

func (FollowUpReconcileArgs) Kind() string { return "follow_up_reconcile" }

// closeDateSweepWorker delegates to the deals corrector — the domain seam
// is River-agnostic; only this adapter knows about River.
type closeDateSweepWorker struct {
    river.WorkerDefaults[CloseDateSweepArgs]
    corrector *deals.CloseDateCorrector
}

func (w *closeDateSweepWorker) Work(ctx context.Context, _ *river.Job[CloseDateSweepArgs]) error {
    return w.corrector.Sweep(ctx)
}

type followUpReconcileWorker struct {
    river.WorkerDefaults[FollowUpReconcileArgs]
    reconciler *deals.FollowUpReconciler
}

func (w *followUpReconcileWorker) Work(ctx context.Context, _ *river.Job[FollowUpReconcileArgs]) error {
    return w.reconciler.Reconcile(ctx)
}

// NewJobRunner wires the deals correctors into River periodic jobs. The
// intervals keep the operator-facing --close-date-interval /
// --reconcile-interval flags as the schedule source; RunOnStart preserves
// the ticker's boot-time first pass; the unique-by-state guard reproduces
// the one-pass-at-a-time behaviour across replicas.
func NewJobRunner(pool *pgxpool.Pool, log *slog.Logger, closeDateInterval, reconcileInterval time.Duration) (*jobs.Runner, error) {
    workers := river.NewWorkers()
    river.AddWorker(workers, &closeDateSweepWorker{corrector: NewCloseDateCorrector(pool, log)})
    river.AddWorker(workers, &followUpReconcileWorker{reconciler: NewFollowUpReconciler(pool, log)})

    unique := func() (river.JobArgs, *river.InsertOpts) { return nil, nil } // replaced per job below

    periodic := []*river.PeriodicJob{
        river.NewPeriodicJob(
            river.PeriodicInterval(closeDateInterval),
            func() (river.JobArgs, *river.InsertOpts) {
                return CloseDateSweepArgs{}, &river.InsertOpts{UniqueOpts: river.UniqueOpts{ByState: river.UniqueStatesDefault}}
            },
            &river.PeriodicJobOpts{RunOnStart: true},
        ),
        river.NewPeriodicJob(
            river.PeriodicInterval(reconcileInterval),
            func() (river.JobArgs, *river.InsertOpts) {
                return FollowUpReconcileArgs{}, &river.InsertOpts{UniqueOpts: river.UniqueOpts{ByState: river.UniqueStatesDefault}}
            },
            &river.PeriodicJobOpts{RunOnStart: true},
        ),
    }
    _ = unique

    return jobs.New(pool, jobs.Config{
        Queues:       map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: 5}},
        Workers:      workers,
        PeriodicJobs: periodic,
    }, log)
}
```

(Remove the stray `unique` scaffold when writing the real file — it is left
here only to flag that both jobs share the same unique-guard shape; DRY it
into a small local helper if the linter prefers.)

- [ ] **Step 4: Run — expect PASS**

Run: `go test ./internal/compose/ -run Kind -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/compose/jobs.go backend/internal/compose/jobs_test.go
git commit -s -m "feat(compose): River workers + periodic jobs for close-date and reconcile"
```

---

## Task 4: cut `cmd/worker` over and delete the ticker wrappers

**Files:**
- Modify: `backend/cmd/worker/main.go`
- Modify: `backend/internal/modules/deals/closedatesweep.go` (delete `RunCloseDateSweep`)
- Modify: `backend/internal/modules/deals/reconcile.go` (delete `RunFollowUpReconcile`)

**Interfaces:**
- Consumes: `compose.NewJobRunner`, `runner.Start`, `runner.Stop`.

- [ ] **Step 1: Replace the two ticker launches**

In `cmd/worker/main.go` `run()`, delete:

```go
corrector := compose.NewCloseDateCorrector(pool, logger)
background.Go(func() { deals.RunCloseDateSweep(ctx, corrector, cfg.closeDateInterval, logger) })
reconciler := compose.NewFollowUpReconciler(pool, logger)
background.Go(func() { deals.RunFollowUpReconcile(ctx, reconciler, cfg.reconcileInterval, logger) })
```

and add, before the relay `Run` call:

```go
runner, err := compose.NewJobRunner(pool, logger, cfg.closeDateInterval, cfg.reconcileInterval)
if err != nil {
    return err
}
if err := runner.Start(ctx); err != nil {
    return err
}
defer func() {
    // Stop drains in-flight jobs; give it a bounded shutdown window
    // independent of the (already-cancelled) run context.
    stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
    defer cancel()
    if err := runner.Stop(stopCtx); err != nil {
        logger.Warn("stopping job runner", "err", err)
    }
}()
_, _ = fmt.Fprintf(stdout, "worker running River jobs (close-date every %s, reconcile every %s)\n",
    cfg.closeDateInterval, cfg.reconcileInterval)
```

Keep the `--close-date-interval` / `--reconcile-interval` flags exactly as
they are — they now feed `NewJobRunner`. The `deals` import stays only if
still used elsewhere in the file; remove it if it becomes unused.

- [ ] **Step 2: Delete the dead ticker wrappers**

Remove `RunCloseDateSweep` from `deals/closedatesweep.go` and
`RunFollowUpReconcile` from `deals/reconcile.go`. `Sweep`/`Reconcile` and
their constructors stay. Confirm no other caller:

Run: `grep -rn "RunCloseDateSweep\|RunFollowUpReconcile" backend/`
Expected: no matches after deletion.

- [ ] **Step 3: Build + unit tests + arch-lint**

Run: `PATH="$(go env GOPATH)/bin:$PATH" make check`
Expected: PASS (build, vet, lint, arch-lint, unit, contract drift). The
deals integration tests still call `.Sweep()`/`.Reconcile()` directly and
are unaffected.

- [ ] **Step 4: Commit**

```bash
git add backend/cmd/worker/main.go backend/internal/modules/deals/closedatesweep.go backend/internal/modules/deals/reconcile.go
git commit -s -m "refactor(worker): run close-date and reconcile as River jobs; drop the ticker loops"
```

---

## Task 5: behavior-preservation integration proof

**Files:**
- Create: `backend/internal/compose/jobs_integration_test.go` (`//go:build integration`)

**Interfaces:**
- Consumes: `compose.NewJobRunner`, the compose integration harness (the
  same one `closedate_integration_test.go` uses for a seeded workspace +
  pool), River's completion subscription.

- [ ] **Step 1: Write the failing test — same outcome, reached through River**

Reuse the exact fixture + assertion from
`closedate_integration_test.go`'s forecast-bearing case, but reach it by
enqueuing the River job and waiting on the completion channel (no sleep):

```go
//go:build integration

// SPDX-License-Identifier: BUSL-1.1
// SPDX-FileCopyrightText: 2026 Gradion

package compose

func TestRiverCloseDateSweepStagesSameProvisionalAsDirectSweep(t *testing.T) {
    e := newCloseDateEnv(t)                 // same harness as the direct test
    e.seedForecastBearingOverdueDeal(t)     // same fixture

    runner, err := NewJobRunner(e.Pool, quiet, time.Hour, time.Hour)
    if err != nil {
        t.Fatalf("NewJobRunner: %v", err)
    }
    // Subscribe BEFORE Start so no completion is missed.
    sub, cancelSub := runner.SubscribeCompleted() // thin passthrough to client.Subscribe
    defer cancelSub()

    ctx := context.Background()
    if err := runner.Start(ctx); err != nil {
        t.Fatalf("Start: %v", err)
    }
    defer func() { _ = runner.Stop(ctx) }()

    // RunOnStart enqueues both periodic jobs at boot; wait for the
    // close_date_sweep completion, bounded by a deadline — never a sleep.
    waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
    defer cancel()
    awaitKindCompleted(t, waitCtx, sub, "close_date_sweep")

    // Identical assertion to the direct-sweep test:
    e.assertProvisionalCorrectionStaged(t)
}
```

`SubscribeCompleted`/`awaitKindCompleted` are thin helpers over River's
`client.Subscribe(river.EventKindJobCompleted)`; add `SubscribeCompleted`
to `platform/jobs.Runner` (returns the channel + cancel). `awaitKindCompleted`
selects on the channel vs `waitCtx.Done()` and fails on deadline — this is
the sleep-free wait the craft gate requires.

- [ ] **Step 2: Run — expect FAIL** (helper undefined / staging not yet reached)

Run: `MARGINCE_TEST_DSN=… go test -tags integration ./internal/compose/ -run TestRiverCloseDateSweep -v`
Expected: FAIL.

- [ ] **Step 3: Add the `SubscribeCompleted` passthrough to `platform/jobs`**

```go
// SubscribeCompleted delivers job-completion events; callers await a
// specific Kind without polling or sleeping. Subscribe before Start.
func (r *Runner) SubscribeCompleted() (<-chan *river.Event, func()) {
    return r.client.Subscribe(river.EventKindJobCompleted)
}
```

- [ ] **Step 4: Run — expect PASS**

Run: `MARGINCE_TEST_DSN=… go test -tags integration ./internal/compose/ -run TestRiverCloseDateSweep -v`
Expected: PASS — the River-driven pass stages the identical provisional
correction the direct `Sweep` test asserts.

- [ ] **Step 5: Add the uniqueness + drain assertions**

Two more cases in the same file:

- `TestRiverSweepUniquenessDoesNotStack`: enqueue `CloseDateSweepArgs{}`
  twice via `client.Insert` while one is running; assert exactly one
  execution completes (the unique-by-state guard), matching the ticker's
  one-pass-at-a-time.
- `TestRiverRunnerDrainsInFlightOnStop`: enqueue a job, `Start`, then
  `Stop`; assert the job reaches `completed` (drain), proving shutdown loses
  no in-flight work — the strict improvement over the ticker.

- [ ] **Step 6: Full integration lane + commit**

Run: `make test-integration`
Expected: PASS, zero `--- SKIP`.

```bash
git add backend/internal/compose/jobs_integration_test.go backend/internal/platform/jobs/jobs.go
git commit -s -m "test(jobs): prove River swap is behavior-preserving (outcome, uniqueness, drain)"
```

---

## Self-review checklist (run before opening the PR)

- **Scope:** only the two named loops moved; retention, runner scheduler,
  `cg:*` subscribers, and the outbox relay are untouched. ✅ enforced by the
  diff (grep for `retention`, `runner-interval`, `cg:` — no changes).
- **Behavior preservation:** `deals.Sweep`/`Reconcile` unchanged; the direct
  integration tests unchanged and green; the new River test asserts the
  identical staged outcome. ✅
- **Boot parity:** `RunOnStart: true` reproduces the ticker's first-pass-on-boot. ✅
- **No-stack parity:** `UniqueOpts{ByState}` reproduces one-pass-at-a-time. ✅
- **Config parity:** `--close-date-interval` / `--reconcile-interval` kept,
  same 24h defaults. ✅
- **Deployment parity:** River workers run only in `cmd/worker`; `cmd/api`
  unchanged (small-install story intact). ✅
- **Gates:** license headers on new files; no swallowed errors; no
  `time.Sleep` in tests; River tables allowlisted in the tenant-table
  fitness gates; `make check` + `make test-integration` green. ✅
- **Dead code:** `RunCloseDateSweep` / `RunFollowUpReconcile` deleted, no
  remaining callers. ✅

## Open questions to confirm during implementation (not blockers)

1. **River version API surface** — `river.Config.Logger`, `PeriodicJobOpts.RunOnStart`,
   and `UniqueStatesDefault` names/shapes are stable but should be confirmed
   against the pinned version at `go get` time; adjust the snippets if the
   pinned release differs.
2. **Migrator connection** — `rivermigrate` wants a pool/driver; `cmd/migrate`
   uses a single `*pgx.Conn` for the SQL runner. Decide: open a short-lived
   pool for the River step, or thread the existing owner pool. Trivial;
   noted so the implementer doesn't rediscover it.
3. **Which DSN runs the migrator** — River DDL needs the owner role (same as
   core/custom `make migrate`), not the runtime app role. Confirm
   `cmd/migrate` already uses the owner DSN (it does for core/custom).
