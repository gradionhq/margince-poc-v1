# Make targets

The real Makefile is `backend/Makefile`; the root Makefile delegates the
backend targets and adds the frontend lane. In `backend/`, `make` (or `make
help`) lists targets with descriptions. Every target that listing advertises
also runs as `make <name>` from the repo root, which `make-target-parity`
enforces — so a command copied out of here works from either directory.

## Everyday

| Target | What it does |
|---|---|
| `help` | List targets (the default goal) |
| `install` | One-shot fresh-worktree setup (frontend deps + Go gate binaries + git hooks). The factory's `worktree-init` runs this by name |
| `dev` | Full local stack: db-up + migrate + `cmd/api` + `cmd/worker` (always on: the outbox relay + Surface-B runner) + the app on `http://localhost:8080` (the api behind it on `:18080`, proxied through). **Sweeps first** (bare invocation): kills every margince api/worker/vite on the machine — recorded, orphaned, or from another checkout — evicts anything holding `:8080`, and drops stray `margince_dev_*` databases, so one stack runs and the app is always on `:8080`. Boots **cold** — the organization + admin bootstrapped from `config/margince.yaml` and nothing else, so onboarding and empty states are the default; `make seed-dev` adds the demo records on top. Returns when ready; the servers run in the background. `DEV_SLUG=<slug>` gives an isolated `margince_dev_<slug>` on slug-derived ports (two worktrees at once). Activates real AI routing only when every cloud provider bound in `config/ai-routing.yaml` has its BYOK key in the environment / `.env.local` (else the offline fake) |
| `dev-fresh` | `make dev-fresh [DEV_SLUG=<slug>]` — `dev` onto a **rebuilt** database: drops it, re-migrates, and boots the installation a first customer gets. Plain `dev` keeps whatever data is there, so a restart for a backend change never costs you a half-finished record |
| `dev-stop` | `make dev-stop [DEV_SLUG=<slug>] [DROP=1]` — bare, it stops **every** dev stack on the machine and frees the ports (the mirror of what `dev` sweeps); with `DEV_SLUG` just that one. `DROP=1` also drops the per-slug `margince_dev_*` databases — never the shared `margince` |
| `dev-logs` | `make dev-logs [DEV_SLUG=<slug>] [ROLE=api\|worker\|fe\|boot] [LEVEL=debug\|info\|warn\|error] [ALL=1] [FOLLOW=0 N=<n>]` — follow `.tmp/dev/<slug>/dev.log` coloured by process and severity. api, worker and Vite all append to that one file, so `make dev` tags each line with the process that wrote it. At `MARGINCE_LOG_LEVEL=debug` the writer also colours the tag and severity **in the file**, so a plain `tail -f` is readable on its own; at info level the file stays plain text so `grep` and editors see clean lines. This view strips whatever colour is there and repaints, so its filters work either way. The job-queue (River) heartbeat is hidden by default because at `MARGINCE_LOG_LEVEL=debug` it repeats every few seconds and pushes real lines off the screen; `ALL=1` restores it. `LEVEL` is a floor, so `LEVEL=warn` shows warnings **and** errors. A dev view only: the servers' own output is unchanged plain text for a log collector |
| `db-up` / `infra-up` | Start the dev Postgres 16 (pgvector, port 15432) and Redis 7 (port 16379) containers, create the app role (`infra-up` is an alias) |
| `db-init` | (Re)apply `scripts/db-init.sql` to the running Postgres |
| `migrate` | Apply core + custom migrations with the owner DSN |
| `infra-down` | Stop the dev containers but keep the data volumes |
| `clean` | Remove the dev containers **and** the data volumes |

## Factory-compatibility golden commands

These are the target names the dark-factory tooling, its UAT runner, and its
UAT guides call by name (`docs/target-minimum-setup.md §3`). `check-q`,
`check-go`, and `fe-typecheck` are the quiet/scope-aware gate variants;
`test-integration` ends with the literal `OK: integration passed with 0 skips`.

| Target | What it does |
|---|---|
| `check-q` | Quiet `make check` — full log in `.tmp/check.log`, excerpt on failure |
| `check-go` | The Go half of the gate (`make -C backend check`) |
| `fe-install` / `fe-typecheck` | Frontend deps install / `tsc` typecheck (scope-aware FE gates) |

## Gates

| Target | What it does |
|---|---|
| `check` | **The merge gate.** Backend `make check` = build + vet + lint + arch-lint + test + drift. Root `make check` runs that **plus** the craft-doc floor, image pins, contract breaking-change (`oasdiff`), test-lane hygiene, and the file-length ratchet |
| `check-backend` / `check-fe` | The two halves of the root gate, runnable alone: `check-backend` = backend `check` + the root script gates below (what CI's deterministic-gates job runs; it needs no frontend toolchain — `contract-frontend-drift` skips loudly without pnpm); `check-fe` = `frontend-check` with a loud fail if `frontend/node_modules` is missing |
| `build` | `go build ./...` |
| `vet` | `go vet ./...` |
| `test` | Unit tests; the root fitness tests (license header, write shape, architecture, enum sync, `audit_log` enum coherence, contract `$ref` resolution) run uncached |
| `test-integration` | Real-Postgres lane (`-tags integration`): RLS gates, governed-agent loop, HTTP end-to-end. Runs on its own `margince_test` namespace, never the dev `margince` DB, so it can run concurrently with `make dev`. **Parallel** — each package runs on its own throwaway clone db (`CREATE DATABASE … TEMPLATE margince_test`) + private MinIO bucket + its own Redis logical db (1..15 by slot; db 0 stays reserved for `make dev`), so packages share nothing; within a package still `-p 1`. Fails loudly without a database — never skips. Tune concurrency with `INTEGRATION_JOBS=N`. CI additionally slices the lane per test across six runners: `INTEGRATION_SHARD=k/N` runs the k-th deterministic slice (debug a red CI shard locally with exactly that), `INTEGRATION_SHARD_OUT=dir` collects the manifests + coverage pods `scripts/test-integration-reconcile.sh` verifies and merges |
| `test-db-up` | (Re)build the migrated `margince_test` template the parallel lane clones from |
| `test-it` | Run ONE integration package on a throwaway clone (+ own MinIO bucket + Redis db 15): `make test-it DIR=backend/internal/modules/people [RUN=TestName]` |
| `e2e-siteread` | (backend Makefile) Deep-read quality floor vs the real gradion.com (`-tags e2e_llm`): paid, network, opt-in. Judge a candidate model with `MARGINCE_E2E_MODEL=provider:model` (+ its BYOK key) or `MARGINCE_AI_ROUTING`; every assertion is a floor — a different model must extract the same or better to pass |
| `e2e-ai` | Certify AI tasks against the corpus (`-tags e2e_llm`, `TestE2ECertify`): paid, network, opt-in. Defaults `MARGINCE_AI_ROUTING` to `config/ai-routing.yaml` (the file `make install` seeds) so it runs out of the box; narrow with `TASK=<task>`, override the candidate with `MODEL=<provider:model>`, repeat with `RUNS=<n>`. Dumps every candidate+judge request/response (the `ai_call_payload` shape, post-stripper) to a gitignored `.tmp/aicert/*.jsonl` and prints the path — on by default (`TRACE=<dir>` to relocate, `TRACE=` to disable). Fails loudly — never skips — without a routing file or a corpus match |
| `e2e-ai-report` | Print the certification readiness report: one row per shipped invocation site (from the census, so a MISSING record is visible), its band, that site's own runs/passed and the accepted/wrong_answer/invalid/abstained counts behind them, the scope certified (`full_invocation`, `single_turn` or `single_call`), and the (provider, model, env) it was measured on. A record whose stamp no longer matches what this build sends — the scenarios, or the prompts its own code builds from them — renders `stale`, one that was never produced renders `absent` — never collapsed, and never as a row of zeroes. Reads `backend/internal/compose/aicert/{corpus,records}/`. `go run`-only dev tool (`internal/compose/aicert/reportcmd`), not a shipped binary and **not a merge gate**: it always exits 0, because the lane it reports on is paid and manual |
| `ai-probe` | Probe ONE production AI invocation site against input an operator supplies, through the same certification case `make e2e-ai` drives (`Prepare`/`Run`/`Evaluate`). Answers a different question from `e2e-ai`: that one asks whether a model is good enough for a prompt, this asks whether a site survives THIS input — which is how a site can be certified 1.00 and still fail in the field. Four verbs via `ARGS=`: `list` (every registered site with its kind, certified scope and tier ladder), `scaffold <task>/<variant>` (a starter scenario copied from the corpus), `fetch <url>` (what crosses the fetch boundary — HTML reduced by `StripTags`, markdown and JSON verbatim — reporting media type, bytes and **passage count**; a route may reduce further before building its request, as the model-cost refresh does for a JSON catalog), and `run` (`--scenario` or `--fixture`+`--expect`+`--site`). Free except `run` against a real binding, which makes one model call — no judge, no scoring, no records written. DB-less. Artifacts land in the gitignored `.tmp/aitask/` because a fetched page carries whatever the source carried. BYOK key auto-loaded from `.env.local`. See [debug an AI task](../how-to/debug-an-ai-task.md) |
| `test-integration-serial` | Escape hatch: the old sequential lane on the shared `margince_test` DB (for debugging a parallel-isolation issue) |
| `lint` | `golangci-lint run` (depguard, gosec, misspell, revive, gofmt), through the `scripts/run-golangci.sh` wrapper `lint-modules` also uses — see `test-golangci-guard` for what it stops the shared analysis cache doing |
| `arch-lint` | go-arch-lint over `.go-arch-lint.yml` — a hard gate on the import DAG |
| `gen` | Regenerate everything derived from `api/crm.yaml` (contract types, 501 stubs, agent-policy table) and the extension composition |
| `drift` | `gen`, then fail if any generated file changed — the contract drift gate |
| `composition` | Materialize `build/composition/` from the enabled set under `extensions/`. Every build/test lane depends on it and runs under `GOWORK=build/composition/go.work`, so an enabled extension is compiled in and a stale composition is never built. A default checkout composes `{de}` (the first-party pack ships enabled); removing every directory under `extensions/` composes the empty set, whose wiring is byte-identical to the committed `composition/` stub |
| `check-composition` | `composition`, then `gen-composition -verify`: a clean regeneration must reproduce `composition.json`'s recorded input digests and output hashes byte-for-byte (the drift gate for ignored composition output) |
| `test-extensions` | Every enabled extension's own test lane (each unit under `extensions/` is its own Go module — `./...` never reaches them), run on the composed workspace; part of `make check` |
| `gen-workflow` | `make gen-workflow NAME=<snake_case_handler_name>` — scaffold a new automation `workflow.Handler` + its test stub (write-once; refuses to overwrite an existing scaffold). See [how-to/create-a-workflow.md](../how-to/create-a-workflow.md) |

The root `make check` runs the backend gate above **and** these deterministic
root gates (each is a small script; all merge-blocking):

| Target | What it does |
|---|---|
| `check-image-pins` | Every workflow `uses:` and container `image:` is pinned to an immutable ref |
| `check-host-ports` | Every host port published by `infra/docker-compose.dev.yml` is below the ephemeral floor (32768), so `db-up` cannot lose a bind to a transient client port |
| `make-target-parity` | Every backend target `make help` advertises resolves from the repo root, as the help text promises. The root delegation list is hand-maintained, so a new backend target can be advertised and unreachable at once — and a CI step that calls it then fails at `No rule to make target` without ever running what it was gating |
| `contract-breaking-check` | oasdiff severity gate on `api/crm.yaml` vs `origin/main` (breaking change fails; additive passes) |
| `contract-frontend-drift` | The **third** regeneration a `backend/api/crm.yaml` change owes: `pnpm gen:api` writes `frontend/src/api/schema.d.ts` + `public-events.ts`, and until #1639 only the frontend lane enforced it — so a backend-only author could go green through the whole backend gate and strand the frontend types (#1573). Skips **loudly** when `pnpm` is absent (CI's `deterministic-gates` job installs Go only, and takes that path). The pull-request path is covered instead by `fe-quality`'s `fe-drift`, whose routing `TestTheContractReachesTheFrontendLane` pins. `fe-drift` runs this same script, so both lanes have one spelling. A `check-backend` prerequisite |
| `test-contract-frontend-drift` | `contract-frontend-drift`'s own test: the skip is loud, goes to stderr, is the gate's only early exit, and the artifact census precedes it. A gate that may skip is a gate that can skip silently, which is the defect it was written for. A `check-backend` prerequisite |
| `migration-versions` | Every migration version this branch adds is unclaimed on `origin/main` and sorts above the highest one there — per namespace, derived from `backend/migrations/*/`. Two PRs numbering against the same `main` each pass the per-tree loader test and collide only in the merge, which is how core `0240` (and `0248` the next day) ended up claimed twice and left `main` unable to load its own sequence. A version *below* the base's highest fails too, and is the worse case: the runner skips only what its ledger already names, so the migration still applies — but before the base's highest on a fresh database and after it on one already past that point, which leaves two schemas wherever the order matters. Overrides: `MIGRATION_VERSIONS_REQUIRE_BASE=1` (CI; a missing base ref is a broken checkout, not a skip), and a base ref as `$1` |
| `test-lanes` | Hermetic-unit-lane check: no untagged test opens a real Postgres/Redis |
| `env-reads` | OPS-CFG-2: nothing under `backend/internal` reads the environment — config is resolved once at the composition root and injected. Ratcheted via `scripts/env-read-waivers.txt` (pre-existing offenders may shrink, never grow; #1252 burns them down). `cmd/**`, `*_test.go`, `//go:build integration` harnesses and `platform/cliflags` are exempt |
| `gofmt` | Every tracked hand-written Go file is gofmt-clean, in **every** module. Not redundant with `lint-modules`, which enforces the same rule through golangci: golangci needs a type-checkable package and the `fixtures/` units deliberately are not one, while gofmt only has to parse. So this is the one formatting check with no exceptions, and the cheap floor that holds when a module is temporarily unlintable. The file list comes from `git ls-files`, so a new module is covered the day it is committed; generated `*_gen.go`/`*.gen.go` are exempt (their generator owns their bytes) |
| `lint-modules` | golangci-lint over the Go modules the backend lint lane cannot reach. `make -C backend lint` runs `./...` from `backend/`, which stops at the module boundary — `backend/tools`, `cli/craft`, `composition` and each unit under `extensions/` are separate modules and were linted by nothing. Same `backend/.golangci.yml` as the product module (one bar, no second copy to drift); the module list derives from tracked `go.mod` files, so a new module is covered the day it is committed. Runs **uncapped** (`--max-same-issues=0`): golangci's default of 3 hides repeats, and a truncating gate reads like a passing one. Two exclusions, both reasoned in the script — `backend` (already linted twice by its own lane) and `fixtures/` (imports the product module while declaring no require, so it type-checks only inside the harness that composes it; still covered by craft, the license test and `gofmt`) |
| `test-golangci-guard` | Prove `scripts/run-golangci.sh` still tells a finding in this checkout from one golangci's cache remembers from another. Its analysis cache is machine-wide, shared by every worktree, and keyed by file **content**, so an unchanged file has one entry across all of them — carrying the path of whichever worktree filled it. A run that gets that entry can read neither the `//nolint:` directives in the file nor the path-anchored exclusions in `.golangci.yml`, so waived findings come back against a foreign path, under module names that do exist here (issue #1378). The wrapper both lint lanes run through resolves every reported path and quarantines the run (exit 40) instead; this asserts both directions, since a guard that flagged every run — `cli/craft` legitimately reports as `../cli/craft/…` — reads exactly like a working one from the passing side |
| `go-file-length` | Hard 500-LOC cap on hand-written **product** Go, ratcheted via `scripts/go-file-length-waivers.txt`. Test and generated files are exempt here — `*_test.go` is bounded at 1000 lines by the craft gate instead |
| `rls-store-path` | No `internal/modules` statement addresses the superuser pool directly (RLS bypass); `// rls-exempt: <reason>` is the escape for a genuinely cross-workspace query |
| `no-jurisdiction` | No country-specific regulatory identifier (XRechnung/ZUGFeRD/DATEV/…) or ISO-3166 code in core **code**, only in the jurisdiction seam (`internal/modules/de`, `internal/shared/ports/jurisdiction`); statute citations in comments are allowed |
| `one-spelling` | Three choke points, in **code** only: SQLSTATEs are named in `storekit/sqlstate.go` (the code list is READ from that file, never re-typed), a CHECK breach is answered by `httperr`'s constraint net rather than a module's copy of it, and the ISO-4217 shape is `values.ValidCurrency`. A genuine false positive is waived on the line with `// one-spelling-exempt: <reason>` |
| `test-one-spelling` | Points the gate at a throwaway tree (so `make -j` cannot race the real one) and asserts its verdict on the unmodified repo plus nine planted cases: each of the three defects fires, an unrelated SQLSTATE-shaped literal does not, a waiver silences its own line and only that line, and the same tokens inside a line, block and inline-block comment stay silent |
| `money-scale` | An amount in minor units is converted by the one owner of the ISO minor-unit table — `shared/kernel/values` in Go, `src/format/minorunits` in TypeScript — never by a hard-coded power of ten, which is wrong for VND/JPY/KRW (no minor unit) and KWD (three digits). The only gate reading both languages: the scale is a contract between them, and while only one side was currency-aware the two disagreed symmetrically and looked correct. Waived on the line with `// money-scale-exempt: <reason>` |
| `test-money-scale` | Points that gate at a throwaway tree and asserts its verdict on the unmodified repo plus twenty-nine planted cases: divide, multiply and remainder fire in **both** languages and at every power the detector accepts (10, 100, 1000, 10000), including an expression the formatter wrapped across lines and an upper-case identifier; a percentage and a progress-bar width do not; the waiver silences its own line and only that line, a marker inside a string literal waives nothing, a template interpolation is judged as the code it is while the string around it is not, and a grouped literal past any minor unit does not fire; and a `fires` case must see an actual scale finding, so the gate exiting non-zero for an unrelated reason cannot pass for a detection |
| `pkg-freeze` | Published-surface freeze (EXT-P3): apidiff on every `backend/pkg` package vs the merge target (`origin/$GITHUB_BASE_REF` in CI; locally the extensions integration branch, else `origin/main`). **Advisory before the first v1+ release tag** — incompatible changes print, never block (the surface is design-fluid). **Enforcing from v1.0.0** — incompatible changes and removed packages fail; a ratified change is its exact apidiff finding line in `scripts/pkg-freeze-allowlist.txt`, bound to the merge-base sha it was ratified against (superseded entries license nothing and warn); removals are never allowlistable. Overrides: `PKG_FREEZE_MODE=advisory\|enforce`, `PKG_FREEZE_BASE=<ref>` |

## Occasional

| Target | What it does |
|---|---|
| `vuln` | govulncheck over all packages. Not part of `check` — it answers against a database that changes daily, so it runs per-PR in `ci.yml` and again daily against `main` in `scheduled.yml`, which is the only lane that can find a vulnerability disclosed after a merge |
| `hooks` (root) | Point git at `.githooks/` (`core.hooksPath`), arming the diff-scoped pre-push craft gate and the RLS/jurisdiction script gates. Run once after cloning; `make install` does it for you. The backend's own `make -C backend hooks` is a **different** target that installs `scripts/pre-commit` (gofmt + license header) — it does **not** set `core.hooksPath`, so it alone leaves the strict pre-push gate disarmed |
| `check-gates` | The meta-gate lane: the waiver census, the obligations derived from the migrations and the contract, and the walk-scope proofs. A dev-loop convenience — deliberately **not** a `check-backend` prerequisite, since `make -C backend check` already runs these tests uncached |
| `tools` / `tools-go` | Install every gate binary at its pinned version (fresh-machine bootstrap) |
| `migrate-up` / `migrate-down` | Alias for `migrate` / roll back the last migration(s) (`STEPS=n`) |
| `migrate-create` | `make migrate-create NAME=add_renewal_risk` — scaffold a core `.up.sql`/`.down.sql` pair named for the current unix second. The clock, not the next number in a sequence: two branches open at once pick the same number and `main` stops loading once both merge. The four-digit `0001`–`0292` sequence is closed; ten-digit stamps sort above it |
| `run` | `go run ./cmd/api` on `:8080` — no db-up/migrate first |
| `seed-reset` / `seed-dev-db` | Wipe the demo workspace / apply the API-less dev SQL seed |
| `psql` / `redis-cli` | Open a shell on the dev database (owner role) / dev Redis |
| `test-v` / `test-cover` | Verbose unit tests / unit tests with a coverage summary |
| `db-wait` / `infra-logs` / `infra-reset` | Block until Postgres answers / tail the dev-stack logs / wipe volumes and restart the stack |
| `bench-perf` | The PERF benchmark harness on the mid-market tier, writing a record (needs `db-up`; seeds 250k contacts) |
| `bench-perf-check` | The same budgets on the **SMB** tier, writing nothing — what the weekly scheduled workflow runs (needs `db-up`) |
| `bench-record` | PERF-1/PERF-4: record open and save p50/p95/p99, measured over HTTP against the booted app (needs `db-up`) |
| `bench-capture` | CAP-PARAM-1: capture-to-timeline latency, 60 s p95, over the auto-create path (needs `db-up`) |
| `perfdoc` | Re-render `docs/reference/performance-budgets.md` from the committed benchmark records. Every `bench-*` target runs it as its last step, so the page updates on every measurement; run it alone after editing the published-budget table in `backend/tools/gen-perfdoc` |
| `tidy` | `go mod tidy` |

### The `bench` lane — measurements, run by hand

`bench-perf`, `bench-perf-check`, `bench-record` and `bench-capture` all carry
`//go:build integration && bench`, so **no MERGE gate runs them**: not `make
check`, not the integration lane. They report
the numbers behind the budgets `acceptance-standards.md` publishes rather than
gating a merge on them, which is why each prints p50/p95/p99 beside its budget
instead of only passing or failing. `bench-mobile` below is the frontend half of
the same posture.

They are still **type-checked** on every `make check`: `go vet -tags 'integration
bench'` and both golangci passes carry the tag. That is load-bearing rather than
tidiness — nothing scheduled compiles these files, so without it a renamed helper
would break them silently and nobody would find out until the next person ran a
benchmark by hand and had to debug the harness instead of reading a number.

Each target's last step re-renders `docs/reference/performance-budgets.md` from
**every** committed record, not just the one it wrote — so a partial run still
leaves a complete page, with the rows it did not measure keeping their own dates
and their own machines. A budget no record covers renders as `not measured`
rather than being dropped, which is the whole reason the page lists the
published set instead of the measured one.

`bench-perf` is the one thing here a schedule touches, and only through
`bench-perf-check`. PERF-3/PERF-7 used to ride the standing integration lane at
the SMB tier as a canary; it stopped, because it was 37.9 s of every merge gate
— a quarter of its package — for a claim it could not make. A PERF-7 row
measured below mid-market renders `inconclusive`, never `within budget`: the
canary's number satisfies a bound it was not measured against. So the merge gate
paid a mid-market price for an SMB answer.

The weekly scheduled workflow now runs `bench-perf-check` on the SMB tier
instead, which is where drift nobody looks for gets found. Not mid-market: that
tier seeds 250k persons and 500k activities and does not finish inside `go
test`'s 30m budget (measured — SMB 46.6s, mid-market killed at 1800.7s on a fast
laptop), and a scheduled job that cannot finish does not report "slow", it goes
red and files an issue claiming a budget breach that was never measured. It
writes nothing:
`MARGINCE_BENCH_RECORD=1` is set by `bench-perf` alone, because publishing a
number stays a human's act — a machine must never write its own numbers into the
tree. The write-path regression the standing canary once caught by TIMING OUT
rather than by measuring is held deterministically now, by the `seq_scan` count
in `lastactivity_integration_test.go`.

## Root-only (frontend lane)

| Target | What it does |
|---|---|
| `frontend-check` | The frontend gate, node-only: `fe-ds-gates`, `fe-drift`, `fe-lint`, `fe-unit`, `fe-build` in that order. It is spelled as those five legs rather than inline because CI runs them as three parallel jobs and both callers have to mean the same thing — `TestEveryLocalFrontendGateLegRunsInCI` fails if a leg added here reaches no CI job |
| `fe-ds-gates` | The design-system purity/font-lock/icon-glyph/spacing/space-token/native-control/ext-import script gates, as one target |
| `fe-drift` | The TS type-drift gate: `pnpm gen:api`, then fail if the committed `src/api/schema.d.ts` / `public-events.ts` moved |
| `fe-unit` | The vitest suite. `FE_COVERAGE=1` instruments it so the one run also writes `frontend/coverage/lcov.info` for the `sonarcloud` job — what CI passes, and about a third slower, which is why it is not the default a developer pays. On those runs `frontend/scripts/check-lcov-paths.sh` reads the report back before anything ships it: the scanner drops a path it cannot resolve in silence, so an unchecked lcov and an untested frontend look identical downstream (#1541) |
| `fe-clock-drift` | The same vitest suite, run as if it were `FE_CLOCK_SKEW_DAYS` (200) from now, and required to reach the same verdict. Not part of `frontend-check` and not a PR gate: the change that breaks these tests is the calendar rather than a diff, so it runs daily on `main` from `scheduled.yml`. It exists because three tests began failing on a date nobody edited anything on (#1977) — a fixture carried an absolute expiry the component compares to `now`. A grep cannot replace it: "an absolute date in a file that never pins the clock" matches 129 files here, nearly all harmless, and nothing static separates a date a component FORMATS from one it COMPARES |
| `fe-quality` | The CI aggregate: every leg of the gate except the unit suite and the bundle, plus the composed-SPA typecheck and the unit screens' suites. Needs a Go toolchain (it composes) |
| `fe-bundle` | The CI aggregate: `fe-build` + `fe-storybook` |
| `fe-install` / `fe-lint` / `fe-test` / `fe-build` / `fe-storybook` / `fe-format` / `fe-preview` | The individual frontend steps (`pnpm` wrappers) |
| `ds-purity` / `font-lock` / `icon-lint` / `ds-spacing` / `native-controls` | The design-system script gates, runnable alone. `native-controls` is the no-browser-drawn-dropdown gate: `<select>`, `<option>` or `<optgroup>` anywhere under `frontend/src` outside `design-system/select.tsx` |
| `gen-types` / `gen-types-check` | Aliases for backend `gen` / `drift` |
| `seed-dev` | API-seed the demo workspace against a running stack (idempotent), then the API-less extras (`seed-dev-db`) |
| `verify-boot` | Prove a running, seeded stack end to end: seeded-admin login, seeded people over `/v1`, frontend production build — pure client, fails loudly |
| `ai-routing-local` | Seed the gitignored `config/ai-routing.yaml` from the committed template on first run (never clobbers an existing copy) |
| `frontend-e2e` | The screen-acceptance UAT harness: AC-named tests + 390px sweep + axe WCAG 2.2 AA + perceived-perf budgets, against the built app over the seed mock (`BASE_URL=…` targets a live backend). Wired into CI as the `uat` job |
| `bench-mobile` | MOBILE-AC-2: record open p95 against the 300 ms **perceived** budget on a throttled Fast-3G profile at 390px (MOBILE-PARAM-2). The by-hand frontend measurement, and the throttled half of PERF-1's perceived budget — the unthrottled half stays a normal AC test in `e2e/ac.spec.ts`. Switched on by `MARGINCE_BENCH_MOBILE=1`, which is what keeps the two runs from collecting each other's specs: `pnpm e2e` does not see `perf-mobile.spec.ts`, and this target sees nothing else |
| `storybook` | The component workbench on `:6006` — the design-system catalog and the story surface `fe-uat` renders. Stories live beside their component as `<name>.stories.tsx` |
| `fe-uat` | Change-scoped Storybook render+capture UAT for frontend-only diffs: renders THIS branch's changed component's stories in headless Chromium and screenshots them (no live stack, no DB). Fails on an unclean render, an unregistered story, or a changed component with no story. Artifact: `.tmp/fe-uat/manifest.json`. Deliberately **not** in `make check` — the fe-only UAT lane a coordinator runs instead of the full stack. `ARGS="--allow-missing"` |

## Isolated stack per worktree

`make dev DEV_SLUG=<slug>` runs a full stack that won't collide with another
worktree's: the ONE shared infra (Postgres/Redis on `15432`/`16379`), but a
private database `margince_dev_<slug>` and api/FE ports derived
deterministically from the slug (the FE's `/v1` proxy follows the api via
`BACKEND_PORT`). Logs + stop handle live under `.tmp/dev/<slug>/`. Bare
`make dev` uses the shared `margince` database with the app on the base
`:8080`. Stop either with `make dev-stop [DEV_SLUG=<slug>] [DROP=1]`.

A slugged stack is the one thing `make dev`'s sweep does not perform — it
sweeps nothing itself, so it can start alongside the base stack. But the next
**bare** `make dev` takes it down and drops its database: exclusivity is the
whole point of the sweep, and an isolated env is a deliberate, temporary
exception to it.

## Root-only (craftsmanship gate)

| Target | What it does |
|---|---|
| `craft-static` | Full deterministic craftsmanship sweep of `backend/`, `extensions/` and `fixtures/` (each a separate Go module, so `./...` never reaches the latter two), **strict**: BLOCKER and MAJOR findings both fail it, MINOR is advisory. Green — the backlog was cleared to arm this bar. The pre-push hook runs the same bar diff-scoped, and CI's `craftsmanship` job runs this target as a required check. Size ceilings: 80 code lines / 500 file lines for product code, 160 / 1000 for `*_test.go`; a comment-only line is not length, so this check agrees with golangci's `funlen` (`ignore-comments`). Every threshold has a flag: `--max-func-lines`, `--max-file-lines`, `--max-test-func-lines`, `--max-test-file-lines` |
| `craft-test` | `cli/craft`'s own suite, including the `wiring` package that asserts the repo-level obligations no Go package can express: the CI job ordering (craftsmanship runs only after the deterministic gates), the contributor rulebook, and the community-health files a newcomer arrives through. It needs its own target because every other Go test lane runs `./...` inside the backend module, which cannot reach a separate module — without this, the suite exists and never runs. A `check-backend` prerequisite |
| `craft-residue` | Fail if any unresolved `CRAFT-FIX`/`CRAFT-DISPUTE` review-loop marker is left in the backend tree. CI's `craft-residue` job runs it on **every** non-draft change, docs included |
| `secret-scan` | No hardcoded credential reaches `main`: gitleaks over a clean `git archive HEAD` export, policy in `.gitleaks.toml`. Scans the committed tree, not the working tree — gitleaks ignores `.gitignore`, so an in-place scan would read a sibling worktree or your real `.env.local` and differ per machine. Installs nothing on your machine and needs no account: `scripts/gitleaks-pin.sh` fetches the version- and checksum-pinned scanner into `.tmp/` on first use, so the binary — and therefore the verdict — is the one CI's `secret-scan` job runs on every non-draft change |
| `test-api-entrypoint` | Prove `scripts/deploy/api-entrypoint.sh` writes the bootstrap admin credential **only** onto an unprovisioned installation, retires one a previous boot left, and refuses to start when its probe cannot answer. Stubs `margince-migrate`/`margince-api` on `PATH`, so it needs no container and no database. Every failure on that path is silent — a credential written to a live installation looks exactly like one that was not — and the entrypoint runs where nobody is watching. CI runs it beside the secret gate |
| `test-dev-dsn` | Prove `scripts/dev.sh` resolves its DSNs through the same names the binaries read (`MARGINCE_OWNER_DSN` / `MARGINCE_DSN`) after an explicit `OWNER_DSN`/`APP_DSN` argument, still names the database itself so a `DEV_SLUG` stack cannot land on the base one, carries a query string like `?sslmode=require` across the swap, and never echoes a DSN. Pure shell — no Docker, no database |
| `test-secret-scan` | Prove `secret-scan` still catches: plant a credential-shaped token in each file `.gitleaks.toml` exempts, and require the scan to fail anyway. An over-broad allowlist reports "no leaks found" exactly like a clean tree — this is the only thing that tells them apart. CI runs it right after the scan |
| `check-craft-doc` | Assert AGENTS.md still carries its `## Craftsmanship` section — a cheap doc floor so the gate's rules cannot be silently unpinned from the rulebook. A `check-backend` prerequisite |

## Root-only (SBOM / supply chain)

| Target | What it does |
|---|---|
| `sbom` | Generate the three source-tree SBOMs (CycloneDX + SPDX 2.2.1 + SPDX 3.0) from a clean `git archive HEAD` export, license-enriched, then normalize and parity-check them. syft/grant/cosign run as digest-pinned Docker images; `jq`, `git` and `tar` run on the host. License enrichment queries the Go module proxy and npm registry, so the run needs network |
| `sbom-normalize` / `sbom-parity` | Reconcile syft's three writers to one repo-relative file set / assert all three enumerate it identically. `sbom` runs both; parity fails the build on any diff |
| `sbom-check` | The license gate — grant against `.grant.yaml` (16 allowed licenses, `require-license` and `require-known-license` both on). Reads the CycloneDX document only |
| `sbom-validate` | Validate each document against its own format — CycloneDX via `cyclonedx validate`, SPDX 2.2.1 via a hash-pinned `pyspdxtools`, SPDX 3.0.1 via the vendored schema in `sbom-schemas/`. Parity proves the three agree; this proves each is well-formed |
| `sbom-sign` | Keyless cosign signature per SBOM (`*.cosign.bundle`); needs an OIDC token, so in practice CI's isolated `sign` job. Depends on `sbom-parity`, never on `sbom` — a signature must cover normalized bytes that already agree |

Full detail: [supply-chain.md](supply-chain.md). This lane is **not** part of `make check`.

## Root-only (desktop build, macOS arm64)

Builds the self-contained folder that runs the whole stack with no Docker.
Output lands in `build/desktop/` (git-ignored). Not part of `make check`, and
not run in CI.

| Target | What it does |
|---|---|
| `desktop` | **The whole folder**, at `build/desktop/margince/` (~128 MB). Reuses an already-built Postgres and event bus — `desktop-deps` builds each only when its output is missing, so a routine app rebuild takes seconds instead of paying the ~5-minute Postgres compile again |
| `desktop-rebuild` | Force everything, Postgres and the bus included |
| `desktop-postgres` | The relocatable Postgres 16 + pgvector + contrib (~5 min). Compiles from pinned, checksummed source, rewrites the Mach-O load commands to `@rpath`, re-signs every patched binary (arm64 refuses one whose signature `install_name_tool` invalidated), and fails if anything still links to `/opt/homebrew`, `/usr/local`, or the staging prefix. **Rerun after bumping the pinned versions** in `desktop/build/build-postgres.sh` |
| `desktop-valkey` | The event bus — Valkey, the BSD-licensed drop-in, since Redis 7.4+ ships under RSALv2/SSPL and this binary is redistributed inside a BUSL-1.1 product |
| `desktop-app` | `api`, `worker`, `migrate` (through `build/composition/`, so the enabled `extensions/` units are linked — a bare `go build` would silently ship without them), the frontend, and the launcher |
| `desktop-dist` | Assemble `build/desktop/margince/` and verify every binary's signature. Signing happens in staging, never here: `codesign` reads a folder holding a same-named executable plus a `resources/` subdirectory as a legacy bundle |
| `desktop-clean` | Remove `build/desktop/` entirely |

The built folder cannot run from `build/desktop/` — that path already exceeds
the 103-byte unix socket limit. Copy it somewhere shorter first. How-to:
[build-the-desktop-app.md](../how-to/build-the-desktop-app.md); the why:
[desktop-distribution.md](../explanation/desktop-distribution.md).

## Root-only (desktop build, Windows x64)

The same folder for Windows, at `build/desktop/margince-windows/`. **These
targets must run ON Windows** and shell out to `desktop/build/*.ps1` through
`pwsh`: pgvector has no build system other than `nmake` against MSVC, and the
event bus needs the MSYS2 toolchain, so neither half cross-builds from macOS.
A Windows host is not required to have GNU make, which is why
`desktop/build/build-windows.ps1` is the primary entry point and these are the
convenience wrapper.

| Target | What it does |
|---|---|
| `desktop-win` | **The whole folder.** Stages Postgres and the bus only when they are missing, so a routine app rebuild does not re-download a 310 MB archive or recompile Redis |
| `desktop-win-rebuild` | Force everything, Postgres and the bus included |
| `desktop-win-postgres` | Pin, verify and unpack the upstream PostgreSQL 16 zip, then compile pgvector against it with MSVC and prune to the server tree. Windows resolves DLLs from the loading executable's own directory, so unlike macOS there is nothing to relocate — the compile is only pgvector, which no prebuilt Windows binary provides. **Needs the Visual Studio C++ workload** |
| `desktop-win-bus` | The event bus — Redis 7.2, the last BSD-3 line before the RSALv2/SSPL relicense and the lineage Valkey forked from, since Valkey has no Windows build. Compiled from pinned source under MSYS2, whose runtime DLL travels beside it with its licence. **Needs MSYS2 + `base-devel gcc`** |
| `desktop-win-app` | `api`, `worker`, `migrate` (through `build/composition/`, so the enabled `extensions/` units are linked), the frontend, and the launcher. No signing step: Authenticode needs a purchased certificate, so the first launch warns through SmartScreen |
| `desktop-win-dist` | Assemble the folder and **run each third-party binary out of it** — the Windows equivalent of the macOS signature check, and the only way a missing DLL is caught here rather than on the user's machine |

`desktop-clean` removes `build/desktop/` for both platforms.

## Variables

`GO`, `PG_PORT` (15432), `REDIS_PORT` (16379), `DB_NAME` (margince),
`OWNER_DSN`, `APP_DSN` — all overridable (`make migrate PG_PORT=5432`).
The Makefile exports `MARGINCE_ENV=dev` and the `MARGINCE_TEST_*`
variables so tests find the dev containers.

`make dev` resolves each DSN in the product's own order — an explicit
`OWNER_DSN`/`APP_DSN` argument, else `MARGINCE_OWNER_DSN`/`MARGINCE_DSN` (what
the binaries themselves read), else the compose default. It passes the result as
an explicit `--dsn`, which is why the resolution happens in the script: `--dsn`
outranks the environment, so a value set in `.env.local` was inert for the whole
dev stack before this.

Two things the stack keeps for itself whatever DSN it is handed. It **names the
database**, so `DEV_SLUG=x` reaches `margince_dev_x` on slug-derived ports and
never the base database a supplied DSN happened to name. And `--fresh` still
refuses when the effective owner DSN is not the compose Postgres, because it
drops through the compose container while migrations follow the DSN. A query
string (`?sslmode=require`) survives the swap; a libpq `host=… dbname=…` DSN is
refused, since a database segment that is not there cannot be replaced.
