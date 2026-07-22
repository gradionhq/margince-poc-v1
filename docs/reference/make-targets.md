# Make targets

The real Makefile is `backend/Makefile`; the root Makefile delegates the
common backend targets and adds the frontend lane. In `backend/`, `make`
(or `make help`) lists targets with descriptions; `help`, `vuln`, and
`hooks` are backend-only (`make -C backend <target>` from the root).

## Everyday

| Target | What it does |
|---|---|
| `help` | List targets (the default goal) |
| `install` | One-shot fresh-worktree setup (frontend deps + Go gate binaries + git hooks). The factory's `worktree-init` runs this by name |
| `dev` | Full local stack: db-up + migrate + `cmd/api` + `cmd/worker` (always on: the outbox relay + Surface-B runner) + the app on `http://localhost:8080` (the api behind it on `:18080`, proxied through). **Sweeps first** (bare invocation): kills every margince api/worker/vite on the machine — recorded, orphaned, or from another checkout — evicts anything holding `:8080`, and drops stray `margince_dev_*` databases, so one stack runs and the app is always on `:8080`. Boots **cold** — the organization + admin bootstrapped from `config/margince.yaml` and nothing else, so onboarding and empty states are the default; `make seed-dev` adds the demo records on top. Returns when ready; the servers run in the background. `DEV_SLUG=<slug>` gives an isolated `margince_dev_<slug>` on slug-derived ports (two worktrees at once). Activates real AI routing only when every cloud provider bound in `config/ai-routing.yaml` has its BYOK key in the environment / `.env.local` (else the offline fake) |
| `dev-fresh` | `make dev-fresh [DEV_SLUG=<slug>]` — `dev` onto a **rebuilt** database: drops it, re-migrates, and boots the installation a first customer gets. Plain `dev` keeps whatever data is there, so a restart for a backend change never costs you a half-finished record |
| `dev-stop` | `make dev-stop [DEV_SLUG=<slug>] [DROP=1]` — bare, it stops **every** dev stack on the machine and frees the ports (the mirror of what `dev` sweeps); with `DEV_SLUG` just that one. `DROP=1` also drops the per-slug `margince_dev_*` databases — never the shared `margince` |
| `mcp-inspector` | `make mcp-inspector TOKEN=mgp_… [DEV_SLUG=<slug>] [WORKSPACE=<slug>]` — build `cmd/mcp` and open the MCP Inspector over stdio against the running `make dev` stack. Token comes from `TOKEN=` or `MARGINCE_PASSPORT_TOKEN` in `.env.local`; the DSN is read from the running stack (so `DEV_SLUG` just works). Token + DSN pass through the environment only, never argv |
| `db-up` / `infra-up` | Start the dev Postgres 16 (pgvector, port 55432) and Redis 7 (port 56379) containers, create the app role (`infra-up` is an alias) |
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
| `check-backend` / `check-fe` | The two halves of the root gate, runnable alone: `check-backend` = backend `check` + the root script gates below (what CI's deterministic-gates job runs); `check-fe` = `frontend-check` with a loud fail if `frontend/node_modules` is missing |
| `build` | `go build ./...` |
| `vet` | `go vet ./...` |
| `test` | Unit tests; the root fitness tests (license header, write shape, architecture, enum sync, `audit_log` enum coherence, contract `$ref` resolution) run uncached |
| `test-integration` | Real-Postgres lane (`-tags integration`): RLS gates, governed-agent loop, HTTP end-to-end. Runs on its own `margince_test` namespace, never the dev `margince` DB, so it can run concurrently with `make dev`. **Parallel** — each package runs on its own throwaway clone db (`CREATE DATABASE … TEMPLATE margince_test`) + private MinIO bucket + its own Redis logical db (1..15 by slot; db 0 stays reserved for `make dev`), so packages share nothing; within a package still `-p 1`. Fails loudly without a database — never skips. Tune concurrency with `INTEGRATION_JOBS=N` |
| `test-db-up` | (Re)build the migrated `margince_test` template the parallel lane clones from |
| `test-it` | Run ONE integration package on a throwaway clone (+ own MinIO bucket + Redis db 15): `make test-it DIR=backend/internal/modules/people [RUN=TestName]` |
| `e2e-siteread` | (backend Makefile) Deep-read quality floor vs the real gradion.com (`-tags e2e_llm`): paid, network, opt-in. Judge a candidate model with `MARGINCE_E2E_MODEL=provider:model` (+ its BYOK key) or `MARGINCE_AI_ROUTING`; every assertion is a floor — a different model must extract the same or better to pass |
| `e2e-ai` | Certify AI tasks against the corpus (`-tags e2e_llm`, `TestE2ECertify`): paid, network, opt-in. Defaults `MARGINCE_AI_ROUTING` to `config/ai-routing.yaml` (the file `make install` seeds) so it runs out of the box; narrow with `TASK=<task>`, override the candidate with `MODEL=<provider:model>`, repeat with `RUNS=<n>`. Dumps every candidate+judge request/response (the `ai_call_payload` shape, post-stripper) to a gitignored `.tmp/aicert/*.jsonl` and prints the path — on by default (`TRACE=<dir>` to relocate, `TRACE=` to disable). Fails loudly — never skips — without a routing file or a corpus match |
| `e2e-ai-report` | Print the task×provider×model certification matrix (verdict, reliability, score p50, latency p50) from the committed records under `backend/internal/compose/aicert/records/`. `go run`-only dev tool (`internal/compose/aicert/reportcmd`), not a shipped binary. No records yet (nothing certified) prints a one-line hint instead of an empty table |
| `test-integration-serial` | Escape hatch: the old sequential lane on the shared `margince_test` DB (for debugging a parallel-isolation issue) |
| `lint` | `golangci-lint run` (depguard, gosec, misspell, revive, gofmt) |
| `arch-lint` | go-arch-lint over `.go-arch-lint.yml` — a hard gate on the import DAG |
| `gen` | Regenerate everything derived from `api/crm.yaml` (contract types, 501 stubs, agent-policy table) and the extension composition |
| `drift` | `gen`, then fail if any generated file changed — the contract drift gate |
| `composition` | Materialize `build/composition/` from the enabled set under `extensions/` (ADR-0069). Every build/test lane depends on it and runs under `GOWORK=build/composition/go.work`, so an enabled extension is compiled in and a stale composition is never built; vanilla composes empty with unchanged output |
| `check-composition` | `composition`, then `gen-composition -verify`: a clean regeneration must reproduce `composition.json`'s recorded input digests and output hashes byte-for-byte (the drift gate for ignored composition output) |
| `test-extensions` | Every enabled extension's own test lane (each unit under `extensions/` is its own Go module — `./...` never reaches them), run on the composed workspace; part of `make check` |
| `gen-workflow` | `make gen-workflow NAME=<snake_case_handler_name>` — scaffold a new automation `workflow.Handler` + its test stub (write-once; refuses to overwrite an existing scaffold). See [how-to/create-a-workflow.md](../how-to/create-a-workflow.md) |

The root `make check` runs the backend gate above **and** these deterministic
root gates (each is a small script; all merge-blocking):

| Target | What it does |
|---|---|
| `check-image-pins` | Every workflow `uses:` and container `image:` is pinned to an immutable ref |
| `contract-breaking-check` | oasdiff severity gate on `api/crm.yaml` vs `origin/main` (breaking change fails; additive passes) |
| `test-lanes` | Hermetic-unit-lane check: no untagged test opens a real Postgres/Redis |
| `go-file-length` | Hard 500-LOC cap on hand-written Go, ratcheted via `scripts/go-file-length-waivers.txt` |
| `rls-store-path` | No `internal/modules` statement addresses the superuser pool directly (RLS bypass); `// rls-exempt: <reason>` is the escape for a genuinely cross-workspace query |
| `no-jurisdiction` | No country-specific regulatory identifier (XRechnung/ZUGFeRD/DATEV/…) or ISO-3166 code in core **code**, only in the jurisdiction seam (`internal/modules/de`, `internal/shared/ports/jurisdiction`); statute citations in comments are allowed |
| `pkg-freeze` | Published-surface freeze (ADR-0069 §3, EXT-P3): apidiff on every `backend/pkg` package vs the merge-base (the extensions integration branch while the arc holds there, else `origin/main`) — an incompatible change or removed published package fails, additive growth passes. Deliberate exception: `PKG_FREEZE_BASE=<ref>` |

## Occasional

| Target | What it does |
|---|---|
| `vuln` | govulncheck over all packages (not yet part of `check`; CI wiring comes later) |
| `hooks` | Install `scripts/pre-commit` (gofmt + license-header test) into git's resolved hooks dir (honors `core.hooksPath`) |
| `tools` / `tools-go` | Install every gate binary at its pinned version (fresh-machine bootstrap) |
| `migrate-up` / `migrate-down` | Alias for `migrate` / roll back the last migration(s) (`STEPS=n`) |
| `run` | `go run ./cmd/api` on `:8080` — no db-up/migrate first |
| `seed-reset` / `seed-dev-db` | Wipe the demo workspace / apply the API-less dev SQL seed |
| `psql` / `redis-cli` | Open a shell on the dev database (owner role) / dev Redis |
| `test-v` / `test-cover` | Verbose unit tests / unit tests with a coverage summary |
| `db-wait` / `infra-logs` / `infra-reset` | Block until Postgres answers / tail the dev-stack logs / wipe volumes and restart the stack |
| `bench-perf` | The PERF benchmark harness on the mid-market tier (needs `db-up`; seeds 250k contacts) |
| `tidy` | `go mod tidy` |

## Root-only (frontend lane)

| Target | What it does |
|---|---|
| `frontend-check` | The frontend gate: the design-system purity/font-lock/icon-glyph/spacing script gates, a `pnpm gen:api` + `schema.d.ts` drift check, then `pnpm check` (Biome lint + vitest + tsc + vite build) (needs node + pnpm) |
| `fe-install` / `fe-lint` / `fe-test` / `fe-build` / `fe-format` / `fe-preview` | The individual frontend steps (`pnpm` wrappers) |
| `ds-purity` / `font-lock` / `icon-lint` / `ds-spacing` | The design-system script gates, runnable alone |
| `gen-types` / `gen-types-check` | Aliases for backend `gen` / `drift` |
| `seed-dev` | API-seed the demo workspace against a running stack (idempotent), then the API-less extras (`seed-dev-db`) |
| `verify-boot` | Prove a running, seeded stack end to end: seeded-admin login, seeded people over `/v1`, frontend production build — pure client, fails loudly |
| `ai-routing-local` | Seed the gitignored `config/ai-routing.yaml` from the committed template on first run (never clobbers an existing copy) |
| `frontend-e2e` | The screen-acceptance UAT harness: AC-named tests + 390px sweep + axe WCAG 2.2 AA + perceived-perf budgets, against the built app over the seed mock (`BASE_URL=…` targets a live backend). Wired into CI as the `uat` job |
| `storybook` | The component workbench on `:6006` — the design-system catalog and the story surface `fe-uat` renders. Stories live beside their component as `<name>.stories.tsx` |
| `fe-uat` | Change-scoped Storybook render+capture UAT for frontend-only diffs: renders THIS branch's changed component's stories in headless Chromium and screenshots them (no live stack, no DB). Fails on an unclean render, an unregistered story, or a changed component with no story. Artifact: `.tmp/fe-uat/manifest.json`. Deliberately **not** in `make check` — the fe-only UAT lane a coordinator runs instead of the full stack. `ARGS="--allow-missing"` |

## Isolated stack per worktree

`make dev DEV_SLUG=<slug>` runs a full stack that won't collide with another
worktree's: the ONE shared infra (Postgres/Redis on `55432`/`56379`), but a
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
| `craft-static` | Full deterministic craftsmanship sweep of `backend/` (the pre-push hook runs the diff-scoped subset) |

## Variables

`GO`, `PG_PORT` (55432), `REDIS_PORT` (56379), `DB_NAME` (margince),
`OWNER_DSN`, `APP_DSN` — all overridable (`make migrate PG_PORT=5432`).
The Makefile exports `MARGINCE_ENV=dev` and the `MARGINCE_TEST_*`
variables so tests find the dev containers.
